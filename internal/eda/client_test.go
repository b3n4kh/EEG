package eda

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchParsesEDAResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v4/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "test-token"})
		case "/pwa/energycommunities/community-1/kpiData":
			assertBearer(t, r)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"autarky":         27.8,
					"ownConsumption":  34.5,
					"community":       107.4,
					"feed":            203.5,
					"remainingDemand": 279.0,
					"communityGrouped": []map[string]any{
						{"enixiGenerationType": "Photovoltaik", "sum": 107.4},
					},
				},
			})
		case "/pwa/energycommunities/community-1/meterdata":
			assertBearer(t, r)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"s":       true,
				"data": map[string]any{
					"substitutesOrMissingData": true,
					"sumGeneration":            12.5,
					"sumFeed":                  7.0,
					"generationSeries": []map[string]any{
						{"date": "2026-05-06T00:00:00", "value": 12.5, "methods": "L1"},
					},
					"feedSeries": []map[string]any{
						{"date": "2026-05-06T00:00:00", "value": 7.0, "methods": nil},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	from := time.Date(2026, 5, 6, 0, 0, 0, 0, viennaLocation)
	to := time.Date(2026, 5, 6, 23, 45, 0, 0, viennaLocation)
	parsed, err := (Client{Config: Config{
		BaseURL:           server.URL,
		Username:          "user@example.com",
		Password:          "secret",
		CommunityID:       "community-1",
		MeteringPointID:   "EDA_TEST",
		MeteringPointName: "EDA Test",
	}}).Fetch(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.SHA256 == "" {
		t.Fatal("SHA256 is empty")
	}
	if parsed.Meters["EDA_TEST"].DisplayName != "EDA Test" {
		t.Fatalf("meter display name = %q", parsed.Meters["EDA_TEST"].DisplayName)
	}
	if len(parsed.Measurements) != 2 {
		t.Fatalf("measurements = %d, want 2", len(parsed.Measurements))
	}
	if got := parsed.Measurements[0].IntervalStart.UTC().Format(time.RFC3339); got != "2026-05-05T22:00:00Z" {
		t.Fatalf("generation interval UTC = %s", got)
	}
	if parsed.Measurements[0].Quality != "L1" {
		t.Fatalf("generation quality = %q, want L1", parsed.Measurements[0].Quality)
	}
	if parsed.Measurements[1].Quality != "" {
		t.Fatalf("feed quality = %q, want empty for null methods", parsed.Measurements[1].Quality)
	}
	if len(parsed.Summaries) == 0 {
		t.Fatal("summaries is empty")
	}
}

func assertBearer(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q", got)
	}
}
