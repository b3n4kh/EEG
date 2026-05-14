package eda

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ben/eeg-sumsum/internal/db"
	"github.com/ben/eeg-sumsum/internal/imports"
)

const (
	DefaultBaseURL       = "https://prod-api.eda-portal.at/api"
	DefaultPortalBaseURL = "https://prod.eda-portal.at/api"
	defaultGroupBy       = "day"
)

var viennaLocation = mustLoadLocation("Europe/Vienna")
var (
	meteringPointPattern     = regexp.MustCompile(`^AT00[0-9A-Z]{29}$`)
	embeddedCommunityPattern = regexp.MustCompile(`[RG]C[0-9]{6}`)
	nonSlugPattern           = regexp.MustCompile(`[^a-z0-9]+`)
)

const (
	directionConsumption = "CONSUMPTION"
	directionGeneration  = "GENERATION"

	metricTotalConsumptionKey  = "gesamtverbrauch_lt_messung_bei_teilnahme_gem_erzeugung_kwh"
	metricTotalConsumption     = "Gesamtverbrauch lt. Messung (bei Teilnahme gem. Erzeugung) [KWH]"
	metricFactorConsumptionKey = "verbrauch_lt_messung_entsprechend_dem_teilnahmefaktor_je_zp_und_ec_id_kwh"
	metricFactorConsumption    = "Verbrauch lt. Messung entsprechend dem Teilnahmefaktor je ZP und EC-ID [KWH]"
	metricCommunityShareKey    = "anteil_gemeinschaftliche_erzeugung_kwh"
	metricCommunityShare       = "Anteil gemeinschaftliche Erzeugung [KWH]"
	metricOwnCommunityKey      = "eigendeckung_gemeinschaftliche_erzeugung_kwh"
	metricOwnCommunity         = "Eigendeckung gemeinschaftliche Erzeugung [KWH]"
	metricOwnRenewableKey      = "eigendeckung_aus_erneuerbarer_energie_kwh"
	metricOwnRenewable         = "Eigendeckung aus erneuerbarer Energie [KWH]"

	metricCommunityGenerationKey = "gesamte_gemeinschaftliche_erzeugung_kwh"
	metricCommunityGeneration    = "Gesamte gemeinschaftliche Erzeugung [KWH]"
	metricGenerationFactorKey    = "erzeugung_lt_messung_entsprechend_dem_teilnahmefaktor_und_ec_id_kwh"
	metricGenerationFactor       = "Erzeugung lt. Messung entsprechend dem Teilnahmefaktor und EC-ID [KWH]"
	metricCommunitySurplusKey    = "gesamt_ueberschusserzeugung_gemeinschaftsueberschuss_kwh"
	metricCommunitySurplus       = "Gesamt/Überschusserzeugung, Gemeinschaftsüberschuss [KWH]"
	metricResidualSurplusKey     = "restueberschuss_bei_eg_und_je_zp_kwh"
	metricResidualSurplus        = "Restüberschuss bei EG und je ZP [KWH]"
)

type Config struct {
	BaseURL        string
	PortalBaseURL  string
	Username       string
	Password       string
	CommunityID    string
	MeteringPoints string
	GroupBy        string
}

func (c Config) Enabled() bool {
	return strings.TrimSpace(c.Username) != "" &&
		strings.TrimSpace(c.Password) != "" &&
		strings.TrimSpace(c.CommunityID) != ""
}

func (c Config) normalized() Config {
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	c.PortalBaseURL = strings.TrimRight(strings.TrimSpace(c.PortalBaseURL), "/")
	if c.PortalBaseURL == "" {
		c.PortalBaseURL = DefaultPortalBaseURL
	}
	c.Username = strings.TrimSpace(c.Username)
	c.Password = strings.TrimSpace(c.Password)
	c.CommunityID = strings.TrimSpace(c.CommunityID)
	c.MeteringPoints = strings.TrimSpace(c.MeteringPoints)
	c.GroupBy = strings.TrimSpace(c.GroupBy)
	if c.GroupBy == "" {
		c.GroupBy = defaultGroupBy
	}
	return c
}

type Client struct {
	Config     Config
	HTTPClient *http.Client
}

func (c Client) Fetch(ctx context.Context, from, to time.Time) (imports.ParsedFile, error) {
	cfg := c.Config.normalized()
	slog.Info("starting EDA import",
		"community_id", cfg.CommunityID,
		"group_by", cfg.GroupBy,
		"from", formatEDATime(from),
		"to", formatEDATime(to),
		"base_url", cfg.BaseURL,
		"portal_base_url", cfg.PortalBaseURL,
	)
	if !cfg.Enabled() {
		return imports.ParsedFile{}, errors.New("EDA import is not configured")
	}
	if cfg.GroupBy != defaultGroupBy {
		return imports.ParsedFile{}, fmt.Errorf("EDA import only supports day-wise data; got groupBy %q", cfg.GroupBy)
	}
	if !to.After(from) && !to.Equal(from) {
		return imports.ParsedFile{}, errors.New("EDA import end must be after start")
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	token, err := login(ctx, httpClient, cfg)
	if err != nil {
		slog.Error("EDA login failed", "error", err)
		return imports.ParsedFile{}, err
	}
	slog.Info("EDA login succeeded", "community_id", cfg.CommunityID)
	points, communityRaw, err := c.meteringPoints(ctx, httpClient, cfg, token)
	if err != nil {
		slog.Error("EDA metering point discovery failed", "community_id", cfg.CommunityID, "error", err)
		return imports.ParsedFile{}, err
	}
	requestBody := requestPayload{
		EnergyCommunityID: cfg.CommunityID,
		GroupBy:           cfg.GroupBy,
		Time: requestTime{In: requestRange{
			Min: formatEDATime(from),
			Max: formatEDATime(to),
		}},
	}
	parsed, err := c.fetchPoints(ctx, httpClient, cfg, token, from, to, requestBody, points, communityRaw)
	if err != nil {
		slog.Error("EDA point import failed", "community_id", cfg.CommunityID, "error", err)
		return imports.ParsedFile{}, err
	}
	slog.Info("EDA import parsed",
		"community_id", cfg.CommunityID,
		"meters", len(parsed.Meters),
		"participant_accounts", len(parsed.ParticipantAccounts),
		"measurements", len(parsed.Measurements),
		"summaries", len(parsed.Summaries),
		"filename", parsed.Filename,
	)
	return parsed, nil
}

type requestPayload struct {
	EnergyCommunityID string      `json:"energyCommunityId"`
	GroupBy           string      `json:"groupBy"`
	Time              requestTime `json:"time"`
}

type requestTime struct {
	In requestRange `json:"in"`
}

type requestRange struct {
	Min string `json:"min"`
	Max string `json:"max"`
}

type meterPoint struct {
	ID              string
	Direction       string
	NetworkOperator string
}

type communityResponse struct {
	Data struct {
		Meterpoints    []communityMeterPoint `json:"meterpoints"`
		MeteringPoints []communityMeterPoint `json:"meteringPoints"`
	} `json:"data"`
}

type communityMeterPoint struct {
	Name            string                `json:"name"`
	MeteringPointID string                `json:"meteringPointId"`
	EnergyDirection string                `json:"energyDirection"`
	IsGenerator     bool                  `json:"isGenerator"`
	GridOperator    string                `json:"gridOperator"`
	NetworkOperator string                `json:"networkOperator"`
	Participant     *communityParticipant `json:"participant"`
}

type communityParticipant struct {
	ID        string            `json:"id"`
	FirstName string            `json:"firstName"`
	LastName  string            `json:"lastName"`
	Address   *communityAddress `json:"address"`
}

type communityAddress struct {
	Street       string `json:"street"`
	StreetNumber string `json:"street_number"`
	City         string `json:"city"`
	Country      string `json:"country"`
	Zip          string `json:"zip"`
}

type loginResponse struct {
	Token string `json:"token"`
	Data  struct {
		Token string `json:"token"`
	} `json:"data"`
}

type edaLocalTime struct {
	time.Time
}

type consumptionResponse struct {
	Success bool             `json:"success"`
	S       bool             `json:"s"`
	Data    []consumptionRow `json:"data"`
	Meta    struct {
		ScaleX string `json:"scale_x"`
	} `json:"meta"`
}

type consumptionRow struct {
	Date  edaLocalTime
	Value float64
}

type seriesPoint struct {
	IntervalStart time.Time
	Value         float64
}

func login(ctx context.Context, httpClient *http.Client, cfg Config) (string, error) {
	body := map[string]string{"email": cfg.Username, "password": cfg.Password}
	slog.Debug("EDA HTTP login request", "url", cfg.BaseURL+"/v4/auth/login", "username", cfg.Username)
	raw, err := postRaw(ctx, httpClient, cfg.BaseURL+"/v4/auth/login", "", body)
	if err != nil {
		return "", fmt.Errorf("EDA login: %w", err)
	}
	var response loginResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", fmt.Errorf("decode EDA login: %w", err)
	}
	token := response.Token
	if token == "" {
		token = response.Data.Token
	}
	if token == "" {
		return "", errors.New("EDA login response did not contain a token")
	}
	return token, nil
}

func (c Client) meteringPoints(ctx context.Context, httpClient *http.Client, cfg Config, token string) ([]meterPoint, []byte, error) {
	if configured := configuredMeteringPoints(cfg.MeteringPoints); len(configured) > 0 {
		slog.Info("using configured EDA metering points", "count", len(configured))
		for _, point := range configured {
			slog.Debug("configured EDA metering point", "metering_point_id", point.ID, "direction", point.Direction, "network_operator", point.NetworkOperator)
		}
		return configured, nil, nil
	}
	apiURL := cfg.BaseURL + "/pwa/energycommunities/" + url.PathEscape(cfg.CommunityID)
	slog.Info("discovering EDA metering points", "url", apiURL)
	raw, err := getRaw(ctx, httpClient, apiURL, token)
	if err != nil {
		return nil, nil, fmt.Errorf("EDA energy community: %w", err)
	}
	slog.Debug("EDA energy community response received", "bytes", len(raw))
	points, err := extractMeteringPoints(raw)
	if err != nil {
		return nil, nil, err
	}
	slog.Info("discovered EDA metering points", "count", len(points))
	for _, point := range points {
		slog.Debug("discovered EDA metering point", "metering_point_id", point.ID, "direction", point.Direction, "network_operator", point.NetworkOperator)
	}
	return points, raw, nil
}

func (c Client) fetchPoints(ctx context.Context, httpClient *http.Client, cfg Config, token string, from, to time.Time, request requestPayload, points []meterPoint, communityRaw []byte) (imports.ParsedFile, error) {
	reportStart := from.In(viennaLocation)
	reportEnd := to.In(viennaLocation)
	parsed := imports.ParsedFile{
		Filename:    filename(cfg, reportStart, reportEnd),
		ReportStart: &reportStart,
		ReportEnd:   &reportEnd,
		DataStart:   &reportStart,
		DataEnd:     &reportEnd,
		Meters:      map[string]db.MeteringPoint{},
	}
	hash := sha256.New()
	_ = json.NewEncoder(hash).Encode(request)
	hash.Write([]byte{0})
	hash.Write(communityRaw)

	for _, point := range points {
		point.Direction = normalizeDirection(point.Direction)
		if point.Direction == "" {
			slog.Error("EDA metering point has no recognized direction", "metering_point_id", point.ID)
			return imports.ParsedFile{}, fmt.Errorf("EDA metering point %s has no recognized direction; configure it explicitly with EDA_METERING_POINTS=%s:CONSUMPTION or %s:GENERATION", point.ID, point.ID, point.ID)
		}
		if point.NetworkOperator == "" && len(point.ID) >= 8 {
			point.NetworkOperator = point.ID[:8]
		}
		parsed.Meters[point.ID] = db.MeteringPoint{
			ID:              point.ID,
			Direction:       point.Direction,
			NetworkOperator: point.NetworkOperator,
		}
		slog.Info("fetching EDA metering point series", "metering_point_id", point.ID, "direction", point.Direction)
		gRaw, gSeries, err := c.fetchConsumptionSeries(ctx, httpClient, cfg, token, "g", point.ID, request)
		if err != nil {
			return imports.ParsedFile{}, err
		}
		pRaw, pSeries, err := c.fetchConsumptionSeries(ctx, httpClient, cfg, token, "p", point.ID, request)
		if err != nil {
			return imports.ParsedFile{}, err
		}
		hash.Write([]byte{0})
		hash.Write([]byte(point.ID))
		hash.Write([]byte{0})
		hash.Write(gRaw)
		hash.Write([]byte{0})
		hash.Write(pRaw)
		parsed.Measurements = append(parsed.Measurements, pointMeasurements(point, gSeries, pSeries)...)
		parsed.Summaries = append(parsed.Summaries, pointSummaries(point, reportStart, reportEnd, gSeries, pSeries)...)
		slog.Info("fetched EDA metering point series",
			"metering_point_id", point.ID,
			"direction", point.Direction,
			"g_points", len(gSeries),
			"g_sum", sumSeries(gSeries),
			"p_points", len(pSeries),
			"p_sum", sumSeries(pSeries),
		)
	}
	parsed.Measurements = append(parsed.Measurements, totalMeasurements(parsed.Measurements)...)
	parsed.Meters["TOTAL"] = db.MeteringPoint{ID: "TOTAL"}
	parsed.ParticipantAccounts = participantAccountsFromCommunity(communityRaw)
	parsed.SHA256 = hex.EncodeToString(hash.Sum(nil))
	return parsed, nil
}

func (c Client) fetchConsumptionSeries(ctx context.Context, httpClient *http.Client, cfg Config, token, kind, meteringPointID string, request requestPayload) ([]byte, []seriesPoint, error) {
	body := struct {
		Time              requestTime `json:"time"`
		GroupBy           string      `json:"groupBy"`
		EnergyCommunityID string      `json:"energyCommunityId"`
		Name              string      `json:"name"`
	}{
		Time:              request.Time,
		GroupBy:           request.GroupBy,
		EnergyCommunityID: cfg.CommunityID,
		Name:              meteringPointID,
	}
	apiURL := cfg.PortalBaseURL + "/consumptionsurya/" + kind + "/" + url.PathEscape(meteringPointID)
	slog.Debug("EDA HTTP series request", "url", apiURL, "kind", kind, "metering_point_id", meteringPointID, "group_by", request.GroupBy, "from", request.Time.In.Min, "to", request.Time.In.Max)
	raw, err := postRaw(ctx, httpClient, apiURL, token, body)
	if err != nil {
		return nil, nil, fmt.Errorf("EDA consumptionsurya/%s/%s: %w", kind, meteringPointID, err)
	}
	var response consumptionResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, nil, fmt.Errorf("decode EDA consumptionsurya/%s/%s: %w", kind, meteringPointID, err)
	}
	if !response.Success || !response.S {
		return nil, nil, fmt.Errorf("EDA consumptionsurya/%s/%s returned success=false", kind, meteringPointID)
	}
	series := make([]seriesPoint, 0, len(response.Data))
	for _, row := range response.Data {
		series = append(series, seriesPoint{IntervalStart: row.Date.Time, Value: row.Value})
	}
	if response.Meta.ScaleX != "" && response.Meta.ScaleX != defaultGroupBy {
		slog.Warn("EDA series response scale differs from requested day grouping", "metering_point_id", meteringPointID, "kind", kind, "scale_x", response.Meta.ScaleX)
	}
	return raw, series, nil
}

func postRaw(ctx context.Context, httpClient *http.Client, apiURL, token string, body any) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Error("EDA HTTP POST failed", "url", apiURL, "status", resp.StatusCode, "response", truncate(raw))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(raw))
	}
	return raw, nil
}

func getRaw(ctx context.Context, httpClient *http.Client, apiURL, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Error("EDA HTTP GET failed", "url", apiURL, "status", resp.StatusCode, "response", truncate(raw))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(raw))
	}
	return raw, nil
}

func pointMeasurements(point meterPoint, gSeries, pSeries []seriesPoint) []db.Measurement {
	switch point.Direction {
	case directionConsumption:
		own := minSeries(gSeries, pSeries)
		out := make([]db.Measurement, 0, len(gSeries)*2+len(pSeries)+len(own)*2)
		out = append(out, seriesMeasurements(point.ID, point.Direction, metricTotalConsumptionKey, metricTotalConsumption, gSeries)...)
		out = append(out, seriesMeasurements(point.ID, point.Direction, metricFactorConsumptionKey, metricFactorConsumption, gSeries)...)
		out = append(out, seriesMeasurements(point.ID, point.Direction, metricCommunityShareKey, metricCommunityShare, pSeries)...)
		out = append(out, seriesMeasurements(point.ID, point.Direction, metricOwnCommunityKey, metricOwnCommunity, own)...)
		out = append(out, seriesMeasurements(point.ID, point.Direction, metricOwnRenewableKey, metricOwnRenewable, own)...)
		return out
	case directionGeneration:
		out := make([]db.Measurement, 0, len(gSeries)*2+len(pSeries))
		out = append(out, seriesMeasurements(point.ID, point.Direction, metricCommunityGenerationKey, metricCommunityGeneration, gSeries)...)
		out = append(out, seriesMeasurements(point.ID, point.Direction, metricGenerationFactorKey, metricGenerationFactor, gSeries)...)
		out = append(out, seriesMeasurements(point.ID, point.Direction, metricResidualSurplusKey, metricResidualSurplus, pSeries)...)
		return out
	default:
		return nil
	}
}

func pointSummaries(point meterPoint, reportStart, reportEnd time.Time, gSeries, pSeries []seriesPoint) []db.OverviewSummary {
	values := map[string]float64{}
	switch point.Direction {
	case directionConsumption:
		own := minSeries(gSeries, pSeries)
		values[metricTotalConsumptionKey] = sumSeries(gSeries)
		values[metricFactorConsumptionKey] = sumSeries(gSeries)
		values[metricCommunityShareKey] = sumSeries(pSeries)
		values[metricOwnCommunityKey] = sumSeries(own)
		values[metricOwnRenewableKey] = sumSeries(own)
	case directionGeneration:
		values[metricCommunityGenerationKey] = sumSeries(gSeries)
		values[metricGenerationFactorKey] = sumSeries(gSeries)
		values[metricResidualSurplusKey] = sumSeries(pSeries)
	}
	out := make([]db.OverviewSummary, 0, len(overviewMetrics()))
	for _, metric := range overviewMetrics() {
		out = append(out, db.OverviewSummary{
			MeteringPointID: point.ID,
			Direction:       point.Direction,
			NetworkOperator: point.NetworkOperator,
			ReportStart:     reportStart,
			ReportEnd:       reportEnd,
			MetricKey:       metric.Key,
			MetricLabel:     metric.Label,
			Value:           values[metric.Key],
			Status:          "Vollständig",
		})
	}
	return out
}

func seriesMeasurements(meteringPointID, direction, metricKey, metricLabel string, series []seriesPoint) []db.Measurement {
	out := make([]db.Measurement, 0, len(series))
	for _, point := range series {
		out = append(out, db.Measurement{
			MeteringPointID: meteringPointID,
			Direction:       direction,
			MetricKey:       metricKey,
			MetricLabel:     metricLabel,
			IntervalStart:   point.IntervalStart,
			Value:           point.Value,
		})
	}
	return out
}

func totalMeasurements(measurements []db.Measurement) []db.Measurement {
	totalKeys := map[string]bool{
		metricTotalConsumptionKey: true,
		metricCommunityShareKey:   true,
		metricOwnRenewableKey:     true,
		metricGenerationFactorKey: true,
		metricResidualSurplusKey:  true,
	}
	type bucketKey struct {
		MetricKey     string
		MetricLabel   string
		IntervalStart time.Time
	}
	buckets := map[bucketKey]float64{}
	for _, measurement := range measurements {
		if !totalKeys[measurement.MetricKey] {
			continue
		}
		key := bucketKey{
			MetricKey:     measurement.MetricKey,
			MetricLabel:   measurement.MetricLabel,
			IntervalStart: measurement.IntervalStart,
		}
		buckets[key] += measurement.Value
	}
	keys := make([]bucketKey, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].MetricKey != keys[j].MetricKey {
			return keys[i].MetricKey < keys[j].MetricKey
		}
		return keys[i].IntervalStart.Before(keys[j].IntervalStart)
	})
	out := make([]db.Measurement, 0, len(keys))
	for _, key := range keys {
		out = append(out, db.Measurement{
			MeteringPointID: "TOTAL",
			MetricKey:       key.MetricKey,
			MetricLabel:     key.MetricLabel,
			IntervalStart:   key.IntervalStart,
			Value:           buckets[key],
		})
	}
	return out
}

func minSeries(left, right []seriesPoint) []seriesPoint {
	rightByTime := map[time.Time]float64{}
	for _, point := range right {
		rightByTime[point.IntervalStart] = point.Value
	}
	out := make([]seriesPoint, 0, len(left))
	for _, point := range left {
		value := point.Value
		if rightValue, ok := rightByTime[point.IntervalStart]; ok && rightValue < value {
			value = rightValue
		}
		out = append(out, seriesPoint{IntervalStart: point.IntervalStart, Value: value})
	}
	return out
}

func sumSeries(series []seriesPoint) float64 {
	total := 0.0
	for _, point := range series {
		total += point.Value
	}
	return total
}

type overviewMetric struct {
	Key   string
	Label string
}

func overviewMetrics() []overviewMetric {
	return []overviewMetric{
		{metricTotalConsumptionKey, metricTotalConsumption},
		{metricFactorConsumptionKey, metricFactorConsumption},
		{metricCommunityShareKey, metricCommunityShare},
		{metricOwnCommunityKey, metricOwnCommunity},
		{metricOwnRenewableKey, metricOwnRenewable},
		{metricCommunityGenerationKey, metricCommunityGeneration},
		{metricGenerationFactorKey, metricGenerationFactor},
		{metricCommunitySurplusKey, metricCommunitySurplus},
		{metricResidualSurplusKey, metricResidualSurplus},
	}
}

func (t *edaLocalTime) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	parsed, err := time.ParseInLocation("2006-01-02T15:04:05", value, viennaLocation)
	if err != nil {
		return err
	}
	t.Time = parsed
	return nil
}

func (r *consumptionRow) UnmarshalJSON(data []byte) error {
	var tuple []json.RawMessage
	if err := json.Unmarshal(data, &tuple); err != nil {
		return err
	}
	if len(tuple) != 2 {
		return fmt.Errorf("expected [date,value] tuple, got %d elements", len(tuple))
	}
	if err := json.Unmarshal(tuple[0], &r.Date); err != nil {
		return err
	}
	if err := json.Unmarshal(tuple[1], &r.Value); err != nil {
		return err
	}
	return nil
}

func formatEDATime(t time.Time) string {
	return t.In(viennaLocation).Format("2006-01-02T15:04")
}

func configuredMeteringPoints(value string) []meterPoint {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var out []meterPoint
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.Split(part, ":")
		point := meterPoint{ID: strings.TrimSpace(fields[0])}
		if len(fields) > 1 {
			point.Direction = normalizeDirection(fields[1])
		}
		if len(fields) > 2 {
			point.NetworkOperator = strings.TrimSpace(fields[2])
		}
		if point.ID == "" {
			continue
		}
		if ok, reason := validMeteringPointID(point.ID); !ok {
			slog.Warn("skipping configured EDA metering point with invalid format", "metering_point_id", point.ID, "reason", reason)
			continue
		}
		out = append(out, point)
	}
	return out
}

func extractMeteringPoints(raw []byte) ([]meterPoint, error) {
	if points, err := extractCommunityMeteringPoints(raw); err == nil && len(points) > 0 {
		return points, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("decode EDA energy community: %w", err)
	}
	points := map[string]meterPoint{}
	collectMeteringPoints(value, "", points)
	out := make([]meterPoint, 0, len(points))
	for _, point := range points {
		if point.Direction == "" {
			point.Direction = normalizeDirection(point.ID)
		}
		if point.ID == "" {
			continue
		}
		out = append(out, point)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) == 0 {
		return nil, errors.New("EDA energy community response did not contain metering points")
	}
	return out, nil
}

func extractCommunityMeteringPoints(raw []byte) ([]meterPoint, error) {
	var response communityResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	rawPoints := response.Data.Meterpoints
	if len(rawPoints) == 0 {
		rawPoints = response.Data.MeteringPoints
	}
	pointsByID := map[string]meterPoint{}
	for _, rawPoint := range rawPoints {
		id := strings.TrimSpace(rawPoint.Name)
		if id == "" {
			id = strings.TrimSpace(rawPoint.MeteringPointID)
		}
		if ok, reason := validMeteringPointID(id); !ok {
			if id != "" {
				slog.Warn("skipping invalid EDA community metering point", "metering_point_id", id, "reason", reason)
			}
			continue
		}
		direction := normalizeDirection(rawPoint.EnergyDirection)
		if direction == "" && rawPoint.IsGenerator {
			direction = directionGeneration
		}
		operator := strings.TrimSpace(rawPoint.GridOperator)
		if operator == "" {
			operator = strings.TrimSpace(rawPoint.NetworkOperator)
		}
		pointsByID[id] = meterPoint{ID: id, Direction: direction, NetworkOperator: operator}
	}
	out := make([]meterPoint, 0, len(pointsByID))
	for _, point := range pointsByID {
		out = append(out, point)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

type participantAccountBuilder struct {
	account imports.ParticipantAccount
	meters  map[string]bool
}

func participantAccountsFromCommunity(raw []byte) []imports.ParticipantAccount {
	if len(raw) == 0 {
		return nil
	}
	var response communityResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		slog.Warn("could not decode EDA community participants", "error", err)
		return nil
	}
	rawPoints := response.Data.Meterpoints
	if len(rawPoints) == 0 {
		rawPoints = response.Data.MeteringPoints
	}
	builders := map[string]*participantAccountBuilder{}
	for _, point := range rawPoints {
		if point.Participant == nil {
			continue
		}
		meterID := strings.TrimSpace(point.Name)
		if meterID == "" {
			meterID = strings.TrimSpace(point.MeteringPointID)
		}
		if ok, reason := validMeteringPointID(meterID); !ok {
			if meterID != "" {
				slog.Debug("skipping EDA participant assignment for invalid metering point", "metering_point_id", meterID, "reason", reason)
			}
			continue
		}
		key := participantIdentityKey(*point.Participant)
		if key == "" {
			slog.Warn("skipping EDA participant without usable identity", "metering_point_id", meterID, "participant_id", point.Participant.ID)
			continue
		}
		builder := builders[key]
		if builder == nil {
			builder = &participantAccountBuilder{
				account: imports.ParticipantAccount{
					Key:         key,
					Username:    participantUsername(*point.Participant),
					DisplayName: participantDisplayName(*point.Participant),
				},
				meters: map[string]bool{},
			}
			builders[key] = builder
		}
		builder.meters[meterID] = true
	}
	out := make([]imports.ParticipantAccount, 0, len(builders))
	for _, builder := range builders {
		for meterID := range builder.meters {
			builder.account.MeteringPointIDs = append(builder.account.MeteringPointIDs, meterID)
		}
		sort.Strings(builder.account.MeteringPointIDs)
		out = append(out, builder.account)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DisplayName != out[j].DisplayName {
			return out[i].DisplayName < out[j].DisplayName
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func participantIdentityKey(participant communityParticipant) string {
	parts := participantIdentityParts(participant)
	if len(parts) == 0 {
		return ""
	}
	return "eda-" + slug(strings.Join(parts, "-"), "-")
}

func participantIdentityParts(participant communityParticipant) []string {
	parts := []string{
		normalizeIdentityPart(participant.FirstName),
		normalizeIdentityPart(participant.LastName),
	}
	if participant.Address != nil {
		parts = append(parts,
			normalizeIdentityPart(participant.Address.Street),
			normalizeIdentityPart(participant.Address.StreetNumber),
			normalizeIdentityPart(participant.Address.Zip),
			normalizeIdentityPart(participant.Address.City),
		)
	}
	out := parts[:0]
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func participantDisplayName(participant communityParticipant) string {
	name := strings.TrimSpace(strings.Join([]string{strings.TrimSpace(participant.FirstName), strings.TrimSpace(participant.LastName)}, " "))
	if name != "" {
		return name
	}
	if participant.ID != "" {
		return participant.ID
	}
	return "Teilnehmer"
}

func participantUsername(participant communityParticipant) string {
	username := slug(strings.TrimSpace(participant.FirstName)+"."+strings.TrimSpace(participant.LastName), ".")
	if username == "" {
		username = slug(participant.ID, ".")
	}
	if username == "" {
		username = "teilnehmer"
	}
	return username
}

func normalizeIdentityPart(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func slug(value, separator string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer(
		"ä", "ae",
		"ö", "oe",
		"ü", "ue",
		"ß", "ss",
	).Replace(value)
	value = nonSlugPattern.ReplaceAllString(value, separator)
	return strings.Trim(value, separator)
}

func collectMeteringPoints(value any, path string, points map[string]meterPoint) {
	switch typed := value.(type) {
	case map[string]any:
		direction := directionHint(path)
		operator := ""
		for key, child := range typed {
			keyLower := strings.ToLower(key)
			if direction == "" {
				direction = directionHint(key)
			}
			if strings.Contains(keyLower, "operator") || strings.Contains(keyLower, "netzbetreiber") {
				if s, ok := child.(string); ok {
					operator = strings.TrimSpace(s)
				}
			}
			if s, ok := child.(string); ok {
				if hinted := directionHint(s); hinted != "" && direction == "" {
					direction = hinted
				}
			}
		}
		for key, child := range typed {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if s, ok := child.(string); ok {
				if accepted, reason := meteringPointCandidate(key, childPath, s); accepted {
					point := points[s]
					point.ID = s
					if point.Direction == "" {
						point.Direction = direction
					}
					if point.NetworkOperator == "" {
						point.NetworkOperator = operator
					}
					points[s] = point
					slog.Debug("accepted EDA metering point candidate", "metering_point_id", s, "path", childPath, "direction_hint", direction, "network_operator", operator)
				} else if reason != "" {
					if strings.Contains(reason, "community code") || isMeteringPointField(key, childPath) {
						slog.Warn("skipped invalid EDA metering point candidate", "value", s, "path", childPath, "reason", reason)
					} else {
						slog.Debug("skipped EDA metering point candidate", "value", s, "path", childPath, "reason", reason)
					}
				}
			}
			collectMeteringPoints(child, childPath, points)
		}
	case []any:
		for i, child := range typed {
			collectMeteringPoints(child, fmt.Sprintf("%s[%d]", path, i), points)
		}
	}
}

func meteringPointCandidate(key, path, value string) (bool, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return false, ""
	}
	if !isMeteringPointField(key, path) && !meteringPointPattern.MatchString(value) {
		return false, ""
	}
	if ok, reason := validMeteringPointID(value); !ok {
		if strings.HasPrefix(value, "AT") || strings.Contains(value, "RC") || strings.Contains(value, "GC") {
			return false, reason
		}
		return false, ""
	}
	if !isMeteringPointField(key, path) {
		return false, "valid-looking metering point appeared outside a metering-point field"
	}
	return true, ""
}

func validMeteringPointID(value string) (bool, string) {
	value = strings.TrimSpace(value)
	if len(value) != 33 {
		return false, fmt.Sprintf("expected 33 characters, got %d", len(value))
	}
	if !strings.HasPrefix(value, "AT00") {
		return false, "expected prefix AT00"
	}
	if !meteringPointPattern.MatchString(value) {
		return false, "expected format AT00 followed by 29 uppercase letters or digits"
	}
	if embedded := embeddedCommunityPattern.FindString(value); embedded != "" {
		return false, "contains embedded community code " + embedded
	}
	return true, ""
}

func isMeteringPointField(key, path string) bool {
	value := strings.ToLower(key + " " + path)
	return strings.Contains(value, "meteringpoint") ||
		strings.Contains(value, "metering_point") ||
		strings.Contains(value, "meterpoint") ||
		strings.Contains(value, "meter_point") ||
		strings.Contains(value, "zählpunkt") ||
		strings.Contains(value, "zaehlpunkt") ||
		strings.Contains(value, "zählpunkte") ||
		strings.Contains(value, "zaehlpunkte")
}

func normalizeDirection(value string) string {
	return directionHint(value)
}

func directionHint(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.Contains(value, "generation"),
		strings.Contains(value, "generator"),
		strings.Contains(value, "producer"),
		strings.Contains(value, "production"),
		strings.Contains(value, "erzeug"):
		return directionGeneration
	case strings.Contains(value, "consumption"),
		strings.Contains(value, "consumer"),
		strings.Contains(value, "verbrauch"),
		strings.Contains(value, "load"):
		return directionConsumption
	default:
		return ""
	}
}

func filename(cfg Config, from, to time.Time) string {
	return fmt.Sprintf("eda_%s_%s_%s.json", cfg.CommunityID, from.Format("20060102"), to.Format("20060102"))
}

func truncate(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "<empty response>"
	}
	if len(text) > 500 {
		return text[:500] + "..."
	}
	return text
}

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.Local
	}
	return loc
}
