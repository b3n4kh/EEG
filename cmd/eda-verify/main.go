package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

const defaultBaseURL = "https://prod-api.eda-portal.at/api"

var viennaLocation = mustLoadLocation("Europe/Vienna")

type options struct {
	baseURL     string
	username    string
	password    string
	communityID string
	from        string
	to          string
	groupBy     string
	endpoint    string
	raw         bool
	discover    bool
	timeout     time.Duration
}

type client struct {
	baseURL     string
	communityID string
	token       string
	httpClient  *http.Client
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

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "eda-verify: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	opts := parseFlags()
	if err := opts.validate(); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	c := &client{
		baseURL:     opts.baseURL,
		communityID: opts.communityID,
		httpClient:  &http.Client{Timeout: opts.timeout},
	}
	expiry, err := c.login(ctx, opts.username, opts.password)
	if err != nil {
		return err
	}
	if expiry.IsZero() {
		fmt.Println("login: ok, token expiry unknown")
	} else {
		fmt.Printf("login: ok, token expires at %s\n", expiry.Format(time.RFC3339))
	}
	if opts.discover {
		if err := c.discover(ctx, opts.raw); err != nil {
			return err
		}
		if opts.communityID == "" {
			return nil
		}
	}

	body := map[string]any{
		"energyCommunityId": opts.communityID,
		"groupBy":           opts.groupBy,
		"time": map[string]any{
			"in": map[string]string{
				"min": opts.from,
				"max": opts.to,
			},
		},
	}
	fmt.Printf("request: community=%s groupBy=%s from=%s to=%s\n", opts.communityID, opts.groupBy, opts.from, opts.to)

	for _, endpoint := range endpointList(opts.endpoint) {
		status, responseBody, err := c.post(ctx, endpoint, body)
		if err != nil {
			return err
		}
		printResponse(endpoint, status, responseBody, opts.raw)
	}
	return nil
}

func parseFlags() options {
	yesterday := time.Now().In(viennaLocation).AddDate(0, 0, -1)
	defaultFrom := yesterday.Format("2006-01-02") + "T00:00"
	defaultTo := yesterday.Format("2006-01-02") + "T23:45"

	opts := options{}
	flag.StringVar(&opts.baseURL, "base-url", envOr("EDA_BASE_URL", defaultBaseURL), "EDA API base URL")
	flag.StringVar(&opts.username, "username", envOr("EDA_USERNAME", ""), "EDA portal username or email")
	flag.StringVar(&opts.password, "password", envOr("EDA_PASSWORD", ""), "EDA portal password")
	flag.StringVar(&opts.communityID, "community-id", envOr("EDA_COMMUNITY_ID", ""), "EDA energy community ID")
	flag.StringVar(&opts.from, "from", envOr("EDA_FROM", defaultFrom), "range start, YYYY-MM-DD or YYYY-MM-DDTHH:MM")
	flag.StringVar(&opts.to, "to", envOr("EDA_TO", defaultTo), "range end, YYYY-MM-DD or YYYY-MM-DDTHH:MM")
	flag.StringVar(&opts.groupBy, "group-by", envOr("EDA_GROUP_BY", "day"), "EDA groupBy value: day, month, or year")
	flag.StringVar(&opts.endpoint, "endpoint", envOr("EDA_ENDPOINT", "both"), "endpoint to verify: both, kpiData, or meterdata")
	flag.BoolVar(&opts.raw, "raw", envBool("EDA_RAW"), "print pretty formatted raw endpoint JSON")
	flag.BoolVar(&opts.discover, "discover", envBool("EDA_DISCOVER"), "try likely endpoints that list accessible communities before verifying data endpoints")
	flag.DurationVar(&opts.timeout, "timeout", envDuration("EDA_TIMEOUT", 30*time.Second), "HTTP timeout")
	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintf(out, "Usage: %s [flags]\n\n", os.Args[0])
		fmt.Fprintln(out, "Verifies the response format of the EDA energy community API without importing data.")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Recommended secret handling:")
		fmt.Fprintln(out, "  EDA_USERNAME='user@example.com' EDA_PASSWORD='...' EDA_COMMUNITY_ID='...' go run ./cmd/eda-verify")
		fmt.Fprintln(out, "  EDA_USERNAME='user@example.com' EDA_PASSWORD='...' go run ./cmd/eda-verify -discover -raw")
		fmt.Fprintln(out)
		flag.PrintDefaults()
	}
	flag.Parse()

	opts.baseURL = strings.TrimRight(strings.TrimSpace(opts.baseURL), "/")
	opts.username = strings.TrimSpace(opts.username)
	opts.communityID = strings.TrimSpace(opts.communityID)
	opts.from = normalizeRangeEndpoint(opts.from, true)
	opts.to = normalizeRangeEndpoint(opts.to, false)
	opts.groupBy = strings.TrimSpace(opts.groupBy)
	opts.endpoint = strings.TrimSpace(opts.endpoint)
	return opts
}

func (opts options) validate() error {
	var missing []string
	if opts.username == "" {
		missing = append(missing, "EDA_USERNAME or -username")
	}
	if opts.password == "" {
		missing = append(missing, "EDA_PASSWORD or -password")
	}
	if opts.communityID == "" && !opts.discover {
		missing = append(missing, "EDA_COMMUNITY_ID or -community-id")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}
	if opts.baseURL == "" {
		return errors.New("base URL is required")
	}
	switch opts.groupBy {
	case "day", "month", "year":
	default:
		return fmt.Errorf("unsupported groupBy %q, expected day, month, or year", opts.groupBy)
	}
	if len(endpointList(opts.endpoint)) == 0 {
		return fmt.Errorf("unsupported endpoint %q, expected both, kpiData, or meterdata", opts.endpoint)
	}
	if opts.timeout <= 0 {
		return errors.New("timeout must be greater than zero")
	}
	return nil
}

func (c *client) login(ctx context.Context, username, password string) (time.Time, error) {
	body := map[string]string{
		"email":    username,
		"password": password,
	}
	status, responseBody, err := postJSON(ctx, c.httpClient, c.baseURL+"/v4/auth/login", "", body)
	if err != nil {
		return time.Time{}, fmt.Errorf("login request: %w", err)
	}
	if status < 200 || status >= 300 {
		return time.Time{}, fmt.Errorf("login failed: HTTP %d: %s", status, truncateForError(responseBody))
	}

	var response map[string]any
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return time.Time{}, fmt.Errorf("decode login response: %w", err)
	}
	token := stringField(response, "token")
	if token == "" {
		if data, ok := response["data"].(map[string]any); ok {
			token = stringField(data, "token")
		}
	}
	if token == "" {
		return time.Time{}, fmt.Errorf("login response did not contain a token; shape:\n%s", responseShape(responseBody))
	}
	c.token = token
	return parseJWTExpiry(token), nil
}

func (c *client) post(ctx context.Context, endpoint string, body any) (int, []byte, error) {
	apiURL := c.baseURL + "/pwa/energycommunities/" + url.PathEscape(c.communityID) + "/" + endpoint
	status, responseBody, err := postJSON(ctx, c.httpClient, apiURL, c.token, body)
	if err != nil {
		return 0, nil, fmt.Errorf("%s request: %w", endpoint, err)
	}
	if status < 200 || status >= 300 {
		return status, responseBody, fmt.Errorf("%s failed: HTTP %d: %s%s", endpoint, status, truncateForError(responseBody), endpointHint(status, responseBody))
	}
	return status, responseBody, nil
}

func (c *client) discover(ctx context.Context, raw bool) error {
	fmt.Println("\n== discovery ==")
	fmt.Println("probing likely endpoints for accessible energy communities")
	candidates := []string{
		"/pwa/energycommunities",
		"/pwa/user/energycommunities",
		"/pwa/energycommunities/list",
		"/pwa/dashboard/energycommunities",
		"/v4/energycommunities",
		"/v4/user/energycommunities",
		"/v4/me",
		"/v4/auth/me",
	}
	for _, path := range candidates {
		status, responseBody, err := getJSON(ctx, c.httpClient, c.baseURL+path, c.token)
		if err != nil {
			fmt.Printf("\nGET %s: error: %v\n", path, err)
			continue
		}
		fmt.Printf("\nGET %s: HTTP %d\n", path, status)
		if status >= 200 && status < 300 {
			fmt.Print(responseShape(responseBody))
			printCandidateIDs(responseBody)
			if raw {
				printRaw(responseBody)
			}
			continue
		}
		if raw || (status != http.StatusNotFound && status != http.StatusMethodNotAllowed) {
			fmt.Println(truncateForError(responseBody))
		}
	}
	return nil
}

func postJSON(ctx context.Context, httpClient *http.Client, apiURL, bearerToken string, body any) (int, []byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, nil, fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(encoded))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read response: %w", err)
	}
	return resp.StatusCode, responseBody, nil
}

func getJSON(ctx context.Context, httpClient *http.Client, apiURL, bearerToken string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read response: %w", err)
	}
	return resp.StatusCode, responseBody, nil
}

func printResponse(endpoint string, status int, body []byte, raw bool) {
	fmt.Printf("\n== %s ==\n", endpoint)
	fmt.Printf("http: %d\n", status)
	fmt.Print(responseShape(body))
	printDomainSummary(endpoint, body)
	if raw {
		printRaw(body)
	}
}

func printDomainSummary(endpoint string, body []byte) {
	switch endpoint {
	case "kpiData":
		var response kpiResponse
		if err := json.Unmarshal(body, &response); err != nil {
			fmt.Printf("typed parse: failed: %v\n", err)
			return
		}
		fmt.Printf("typed parse: success=%t community=%.3f feed=%.3f remainingDemand=%.3f autarky=%.2f%% ownConsumption=%.2f%% grouped=%d\n",
			response.Success,
			response.Data.Community,
			response.Data.Feed,
			response.Data.RemainingDemand,
			response.Data.Autarky,
			response.Data.OwnConsumption,
			len(response.Data.CommunityGrouped),
		)
		for _, group := range response.Data.CommunityGrouped {
			fmt.Printf("  communityGrouped[%s]=%.3f\n", group.EnixiGenerationType, group.Sum)
		}
	case "meterdata":
		var response meterResponse
		if err := json.Unmarshal(body, &response); err != nil {
			fmt.Printf("typed parse: failed: %v\n", err)
			return
		}
		fmt.Printf("typed parse: success=%t s=%t substitutesOrMissingData=%t sumGeneration=%.3f sumFeed=%.3f\n",
			response.Success,
			response.S,
			response.Data.SubstitutesOrMissingData,
			response.Data.SumGeneration,
			response.Data.SumFeed,
		)
		printSeriesSummary("generationSeries", response.Data.GenerationSeries)
		printSeriesSummary("feedSeries", response.Data.FeedSeries)
	}
}

func printSeriesSummary(label string, series []meterSeriesPoint) {
	if len(series) == 0 {
		fmt.Printf("  %s: len=0\n", label)
		return
	}
	total := 0.0
	nullMethods := 0
	methodCounts := map[string]int{}
	for _, point := range series {
		total += point.Value
		if point.Methods == nil {
			nullMethods++
			continue
		}
		methodCounts[*point.Methods]++
	}
	fmt.Printf("  %s: len=%d first=%s last=%s valueSum=%.3f nullMethods=%d methods=%s\n",
		label,
		len(series),
		series[0].Date.Format("2006-01-02T15:04:05-07:00"),
		series[len(series)-1].Date.Format("2006-01-02T15:04:05-07:00"),
		total,
		nullMethods,
		formatMethodCounts(methodCounts),
	)
}

func printRaw(body []byte) {
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, body, "", "  "); err == nil {
		fmt.Println("raw:")
		fmt.Println(pretty.String())
	} else {
		fmt.Println("raw:")
		fmt.Println(string(body))
	}
}

func responseShape(body []byte) string {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return fmt.Sprintf("json: invalid: %v\n", err)
	}
	var b strings.Builder
	writeShape(&b, "$", value, 0)
	return b.String()
}

func writeShape(b *strings.Builder, path string, value any, depth int) {
	if depth > 5 {
		fmt.Fprintf(b, "%s: ...\n", path)
		return
	}
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		fmt.Fprintf(b, "%s: object(keys=%s)\n", path, strings.Join(keys, ","))
		for _, key := range keys {
			writeShape(b, path+"."+key, v[key], depth+1)
		}
	case []any:
		fmt.Fprintf(b, "%s: array(len=%d)\n", path, len(v))
		if len(v) > 0 {
			writeShape(b, path+"[0]", v[0], depth+1)
		}
	case string:
		fmt.Fprintf(b, "%s: string(%q)\n", path, sampleString(v))
	case float64:
		fmt.Fprintf(b, "%s: number(%v)\n", path, v)
	case bool:
		fmt.Fprintf(b, "%s: bool(%v)\n", path, v)
	case nil:
		fmt.Fprintf(b, "%s: null\n", path)
	default:
		fmt.Fprintf(b, "%s: %T\n", path, value)
	}
}

func endpointList(endpoint string) []string {
	switch endpoint {
	case "both":
		return []string{"kpiData", "meterdata"}
	case "kpiData", "meterdata":
		return []string{endpoint}
	default:
		return nil
	}
}

func normalizeRangeEndpoint(value string, start bool) string {
	value = strings.TrimSpace(value)
	if len(value) == len("2006-01-02") && strings.Count(value, "-") == 2 {
		if start {
			return value + "T00:00"
		}
		return value + "T23:45"
	}
	return value
}

func envOr(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func envBool(key string) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return value == "1" || value == "true" || value == "yes"
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.Local
	}
	return loc
}

func stringField(m map[string]any, key string) string {
	value, ok := m[key].(string)
	if !ok {
		return ""
	}
	return value
}

func parseJWTExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

func truncateForError(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "<empty response>"
	}
	if len(text) > 500 {
		return text[:500] + "..."
	}
	return text
}

func endpointHint(status int, body []byte) string {
	if status != http.StatusNotFound || !strings.Contains(string(body), "EnergyCommunity") {
		return ""
	}
	return "\n\nhint: login worked, but this API route did not find that energy community ID. The path likely expects the portal's internal energy community model ID, not the RC/GC market participant code. Run with -discover -raw or open the EDA portal and copy the ID from the energy-community URL."
}

func sampleString(value string) string {
	if len(value) > 80 {
		return value[:80] + "..."
	}
	return value
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

func formatMethodCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", key, counts[key]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func printCandidateIDs(body []byte) {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return
	}
	seen := map[string]bool{}
	var candidates []string
	collectCandidateIDs(value, "", seen, &candidates)
	sort.Strings(candidates)
	if len(candidates) == 0 {
		return
	}
	fmt.Println("candidate id fields:")
	for _, candidate := range candidates {
		fmt.Printf("  %s\n", candidate)
	}
}

func collectCandidateIDs(value any, path string, seen map[string]bool, candidates *[]string) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			lowerKey := strings.ToLower(key)
			if strings.Contains(lowerKey, "id") || lowerKey == "code" || strings.Contains(lowerKey, "participant") {
				switch typed := child.(type) {
				case string:
					addCandidateID(childPath, typed, seen, candidates)
				case float64:
					addCandidateID(childPath, fmt.Sprintf("%.0f", typed), seen, candidates)
				}
			}
			collectCandidateIDs(child, childPath, seen, candidates)
		}
	case []any:
		limit := len(v)
		if limit > 5 {
			limit = 5
		}
		for i := 0; i < limit; i++ {
			collectCandidateIDs(v[i], fmt.Sprintf("%s[%d]", path, i), seen, candidates)
		}
	}
}

func addCandidateID(path, value string, seen map[string]bool, candidates *[]string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	line := path + "=" + value
	if seen[line] {
		return
	}
	seen[line] = true
	*candidates = append(*candidates, line)
}
