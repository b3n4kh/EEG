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
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ben/eeg-sumsum/internal/db"
	"github.com/ben/eeg-sumsum/internal/imports"
)

const (
	DefaultBaseURL = "https://prod-api.eda-portal.at/api"
	defaultGroupBy = "day"
)

var viennaLocation = mustLoadLocation("Europe/Vienna")

type Config struct {
	BaseURL           string
	Username          string
	Password          string
	CommunityID       string
	MeteringPointID   string
	MeteringPointName string
	GroupBy           string
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
	c.Username = strings.TrimSpace(c.Username)
	c.Password = strings.TrimSpace(c.Password)
	c.CommunityID = strings.TrimSpace(c.CommunityID)
	c.MeteringPointID = strings.TrimSpace(c.MeteringPointID)
	if c.MeteringPointID == "" && c.CommunityID != "" {
		c.MeteringPointID = "EDA_" + c.CommunityID
	}
	c.MeteringPointName = strings.TrimSpace(c.MeteringPointName)
	if c.MeteringPointName == "" {
		c.MeteringPointName = "EDA Community " + c.CommunityID
	}
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
	if !cfg.Enabled() {
		return imports.ParsedFile{}, errors.New("EDA import is not configured")
	}
	if cfg.GroupBy != "day" {
		return imports.ParsedFile{}, fmt.Errorf("unsupported EDA groupBy %q for import", cfg.GroupBy)
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
	kpiRaw, err := post(ctx, httpClient, cfg, token, "kpiData", requestBody)
	if err != nil {
		return imports.ParsedFile{}, err
	}
	meterRaw, err := post(ctx, httpClient, cfg, token, "meterdata", requestBody)
	if err != nil {
		return imports.ParsedFile{}, err
	}
	return parse(cfg, from, to, requestBody, kpiRaw, meterRaw)
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

type loginResponse struct {
	Token string `json:"token"`
	Data  struct {
		Token string `json:"token"`
	} `json:"data"`
}

type kpiResponse struct {
	Success bool    `json:"success"`
	Data    kpiData `json:"data"`
}

type kpiData struct {
	Autarky          float64              `json:"autarky"`
	OwnConsumption   float64              `json:"ownConsumption"`
	Community        float64              `json:"community"`
	CommunityGrouped []communityGroupItem `json:"communityGrouped"`
	Feed             float64              `json:"feed"`
	RemainingDemand  float64              `json:"remainingDemand"`
}

type communityGroupItem struct {
	EnixiGenerationType string  `json:"enixiGenerationType"`
	Sum                 float64 `json:"sum"`
}

type meterResponse struct {
	Success bool      `json:"success"`
	S       bool      `json:"s"`
	Data    meterData `json:"data"`
}

type meterData struct {
	SubstitutesOrMissingData bool               `json:"substitutesOrMissingData"`
	SumGeneration            float64            `json:"sumGeneration"`
	SumFeed                  float64            `json:"sumFeed"`
	GenerationSeries         []meterSeriesPoint `json:"generationSeries"`
	FeedSeries               []meterSeriesPoint `json:"feedSeries"`
}

type meterSeriesPoint struct {
	Date    edaLocalTime `json:"date"`
	Value   float64      `json:"value"`
	Methods *string      `json:"methods"`
}

type edaLocalTime struct {
	time.Time
}

func login(ctx context.Context, httpClient *http.Client, cfg Config) (string, error) {
	body := map[string]string{"email": cfg.Username, "password": cfg.Password}
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

func post(ctx context.Context, httpClient *http.Client, cfg Config, token, endpoint string, body any) ([]byte, error) {
	apiURL := cfg.BaseURL + "/pwa/energycommunities/" + url.PathEscape(cfg.CommunityID) + "/" + endpoint
	raw, err := postRaw(ctx, httpClient, apiURL, token, body)
	if err != nil {
		return nil, fmt.Errorf("EDA %s: %w", endpoint, err)
	}
	return raw, nil
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
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(raw))
	}
	return raw, nil
}

func parse(cfg Config, from, to time.Time, request requestPayload, kpiRaw, meterRaw []byte) (imports.ParsedFile, error) {
	var kpi kpiResponse
	if err := json.Unmarshal(kpiRaw, &kpi); err != nil {
		return imports.ParsedFile{}, fmt.Errorf("decode EDA kpiData: %w", err)
	}
	if !kpi.Success {
		return imports.ParsedFile{}, errors.New("EDA kpiData returned success=false")
	}
	var meter meterResponse
	if err := json.Unmarshal(meterRaw, &meter); err != nil {
		return imports.ParsedFile{}, fmt.Errorf("decode EDA meterdata: %w", err)
	}
	if !meter.Success || !meter.S {
		return imports.ParsedFile{}, errors.New("EDA meterdata returned success=false")
	}

	reportStart := from.In(viennaLocation)
	reportEnd := to.In(viennaLocation)
	parsed := imports.ParsedFile{
		SHA256:      responseHash(request, kpiRaw, meterRaw),
		Filename:    filename(cfg, reportStart, reportEnd),
		ReportStart: &reportStart,
		ReportEnd:   &reportEnd,
		DataStart:   &reportStart,
		DataEnd:     &reportEnd,
		Meters: map[string]db.MeteringPoint{
			cfg.MeteringPointID: {
				ID:              cfg.MeteringPointID,
				DisplayName:     cfg.MeteringPointName,
				Direction:       "COMMUNITY",
				NetworkOperator: "EDA Portal",
			},
		},
	}
	parsed.Measurements = append(parsed.Measurements, measurements(cfg, "GENERATION", "eda_generation", "EDA Generation [kWh]", meter.Data.GenerationSeries)...)
	parsed.Measurements = append(parsed.Measurements, measurements(cfg, "FEED", "eda_feed", "EDA Feed [kWh]", meter.Data.FeedSeries)...)
	parsed.Summaries = summaries(cfg, reportStart, reportEnd, kpi, meter)
	return parsed, nil
}

func measurements(cfg Config, direction, metricKey, metricLabel string, series []meterSeriesPoint) []db.Measurement {
	out := make([]db.Measurement, 0, len(series))
	for _, point := range series {
		quality := ""
		if point.Methods != nil {
			quality = *point.Methods
		}
		out = append(out, db.Measurement{
			MeteringPointID: cfg.MeteringPointID,
			Direction:       direction,
			MetricKey:       metricKey,
			MetricLabel:     metricLabel,
			IntervalStart:   point.Date.Time,
			Value:           point.Value,
			Quality:         quality,
		})
	}
	return out
}

func summaries(cfg Config, reportStart, reportEnd time.Time, kpi kpiResponse, meter meterResponse) []db.OverviewSummary {
	values := []struct {
		key   string
		label string
		value float64
	}{
		{"eda_autarky_percent", "EDA Autarky [%]", kpi.Data.Autarky},
		{"eda_own_consumption_percent", "EDA Own Consumption [%]", kpi.Data.OwnConsumption},
		{"eda_community", "EDA Community [kWh]", kpi.Data.Community},
		{"eda_feed", "EDA Feed [kWh]", kpi.Data.Feed},
		{"eda_remaining_demand", "EDA Remaining Demand [kWh]", kpi.Data.RemainingDemand},
		{"eda_sum_generation", "EDA Sum Generation [kWh]", meter.Data.SumGeneration},
		{"eda_sum_feed", "EDA Sum Feed [kWh]", meter.Data.SumFeed},
	}
	out := make([]db.OverviewSummary, 0, len(values)+len(kpi.Data.CommunityGrouped))
	for _, value := range values {
		out = append(out, summary(cfg, reportStart, reportEnd, value.key, value.label, value.value, meter.Data.SubstitutesOrMissingData))
	}
	for _, group := range kpi.Data.CommunityGrouped {
		key := imports.MetricKey("eda_community_grouped_" + group.EnixiGenerationType)
		label := "EDA Community " + group.EnixiGenerationType + " [kWh]"
		out = append(out, summary(cfg, reportStart, reportEnd, key, label, group.Sum, meter.Data.SubstitutesOrMissingData))
	}
	return out
}

func summary(cfg Config, reportStart, reportEnd time.Time, key, label string, value float64, substitutes bool) db.OverviewSummary {
	status := ""
	if substitutes {
		status = "substitutes_or_missing_data"
	}
	return db.OverviewSummary{
		MeteringPointID: cfg.MeteringPointID,
		Direction:       "COMMUNITY",
		NetworkOperator: "EDA Portal",
		ReportStart:     reportStart,
		ReportEnd:       reportEnd,
		MetricKey:       key,
		MetricLabel:     label,
		Value:           value,
		Status:          status,
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

func formatEDATime(t time.Time) string {
	return t.In(viennaLocation).Format("2006-01-02T15:04")
}

func responseHash(request requestPayload, kpiRaw, meterRaw []byte) string {
	hash := sha256.New()
	_ = json.NewEncoder(hash).Encode(request)
	hash.Write([]byte{0})
	hash.Write(kpiRaw)
	hash.Write([]byte{0})
	hash.Write(meterRaw)
	return hex.EncodeToString(hash.Sum(nil))
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
