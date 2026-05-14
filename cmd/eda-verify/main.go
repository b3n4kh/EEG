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
	timeout     time.Duration
}

type client struct {
	baseURL     string
	communityID string
	token       string
	httpClient  *http.Client
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
	loc, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		loc = time.Local
	}
	yesterday := time.Now().In(loc).AddDate(0, 0, -1)
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
	flag.DurationVar(&opts.timeout, "timeout", envDuration("EDA_TIMEOUT", 30*time.Second), "HTTP timeout")
	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintf(out, "Usage: %s [flags]\n\n", os.Args[0])
		fmt.Fprintln(out, "Verifies the response format of the EDA energy community API without importing data.")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Recommended secret handling:")
		fmt.Fprintln(out, "  EDA_USERNAME='user@example.com' EDA_PASSWORD='...' EDA_COMMUNITY_ID='...' go run ./cmd/eda-verify")
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
	if opts.communityID == "" {
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
		return status, responseBody, fmt.Errorf("%s failed: HTTP %d: %s", endpoint, status, truncateForError(responseBody))
	}
	return status, responseBody, nil
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

func printResponse(endpoint string, status int, body []byte, raw bool) {
	fmt.Printf("\n== %s ==\n", endpoint)
	fmt.Printf("http: %d\n", status)
	fmt.Print(responseShape(body))
	if raw {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, body, "", "  "); err == nil {
			fmt.Println("raw:")
			fmt.Println(pretty.String())
		} else {
			fmt.Println("raw:")
			fmt.Println(string(body))
		}
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

func sampleString(value string) string {
	if len(value) > 80 {
		return value[:80] + "..."
	}
	return value
}
