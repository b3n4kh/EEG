package eda

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchParsesEDAResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v4/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "test-token"})
		case "/pwa/energycommunities/community-1":
			assertBearer(t, r)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"meteringPoints": []map[string]any{
						{"meteringPointId": "AT0010000000000000001000000000001", "energyDirection": "CONSUMPTION", "participant": testParticipant("p-1")},
						{"meteringPointId": "AT0010000000000000001000000000002", "energyDirection": "GENERATION", "participant": testParticipant("p-2")},
						{"meteringPointId": "AT00100000000RC107893000000001178", "energyDirection": "GENERATION"},
					},
				},
			})
		case "/consumptionsurya/g/AT0010000000000000001000000000001":
			assertBearer(t, r)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"s":       true,
				"data":    [][]any{{"2026-05-06T00:00:00", 10.0}},
				"meta":    map[string]any{"scale_x": "day"},
			})
		case "/consumptionsurya/p/AT0010000000000000001000000000001":
			assertBearer(t, r)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"s":       true,
				"data":    [][]any{{"2026-05-06T00:00:00", 4.0}},
				"meta":    map[string]any{"scale_x": "day"},
			})
		case "/consumptionsurya/g/AT0010000000000000001000000000002":
			assertBearer(t, r)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"s":       true,
				"data":    [][]any{{"2026-05-06T00:00:00", 20.0}},
				"meta":    map[string]any{"scale_x": "day"},
			})
		case "/consumptionsurya/p/AT0010000000000000001000000000002":
			assertBearer(t, r)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"s":       true,
				"data":    [][]any{{"2026-05-06T00:00:00", 7.0}},
				"meta":    map[string]any{"scale_x": "day"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	from := time.Date(2026, 5, 6, 0, 0, 0, 0, viennaLocation)
	to := time.Date(2026, 5, 6, 23, 45, 0, 0, viennaLocation)
	parsed, err := (Client{Config: Config{
		BaseURL:       server.URL,
		PortalBaseURL: server.URL,
		Username:      "user@example.com",
		Password:      "secret",
		CommunityID:   "community-1",
	}}).Fetch(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.SHA256 == "" {
		t.Fatal("SHA256 is empty")
	}
	if len(parsed.Meters) != 3 {
		t.Fatalf("meters = %d, want 3 including TOTAL", len(parsed.Meters))
	}
	if parsed.Meters["AT0010000000000000001000000000001"].Direction != directionConsumption {
		t.Fatalf("consumption meter direction = %q", parsed.Meters["AT0010000000000000001000000000001"].Direction)
	}
	if parsed.Meters["AT0010000000000000001000000000002"].Direction != directionGeneration {
		t.Fatalf("generation meter direction = %q", parsed.Meters["AT0010000000000000001000000000002"].Direction)
	}
	if len(parsed.Measurements) != 13 {
		t.Fatalf("measurements = %d, want 13", len(parsed.Measurements))
	}
	if got := parsed.Measurements[0].IntervalStart.UTC().Format(time.RFC3339); got != "2026-05-05T22:00:00Z" {
		t.Fatalf("first interval UTC = %s", got)
	}
	if len(parsed.Summaries) != 18 {
		t.Fatalf("summaries = %d, want 18", len(parsed.Summaries))
	}
	if len(parsed.ParticipantAccounts) != 1 {
		t.Fatalf("participant accounts = %d, want 1", len(parsed.ParticipantAccounts))
	}
	account := parsed.ParticipantAccounts[0]
	if account.Username != "petra.akhras" {
		t.Fatalf("participant username = %q, want petra.akhras", account.Username)
	}
	if len(account.MeteringPointIDs) != 2 {
		t.Fatalf("participant meters = %d, want 2", len(account.MeteringPointIDs))
	}
}

func TestFetchRejectsNonDayGroupBy(t *testing.T) {
	_, err := (Client{Config: Config{
		BaseURL:       "http://127.0.0.1",
		PortalBaseURL: "http://127.0.0.1",
		Username:      "user@example.com",
		Password:      "secret",
		CommunityID:   "community-1",
		GroupBy:       "quarter-hour",
	}}).Fetch(context.Background(), time.Now(), time.Now())
	if err == nil || !strings.Contains(err.Error(), "only supports day-wise data") {
		t.Fatalf("err = %v, want day-wise groupBy error", err)
	}
}

func TestValidMeteringPointIDRejectsEmbeddedCommunityCode(t *testing.T) {
	if ok, reason := validMeteringPointID("AT00100000000RC107893000000001178"); ok {
		t.Fatalf("validMeteringPointID accepted embedded community code, reason=%q", reason)
	}
	if ok, reason := validMeteringPointID("AT0010000000000000001000015578460"); !ok {
		t.Fatalf("validMeteringPointID rejected valid point: %s", reason)
	}
}

func TestExtractMeteringPointsSkipsNonMeteringPointFields(t *testing.T) {
	raw := []byte(`{
		"data": {
			"url": "AT0010000000000000001000015578460",
			"meteringPoints": [
				{"meteringPointId": "AT0010000000000000001000015578460", "energyDirection": "CONSUMPTION"}
			]
		}
	}`)
	points, err := extractMeteringPoints(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 {
		t.Fatalf("points = %d, want 1", len(points))
	}
}

func assertBearer(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q", got)
	}
}

func testParticipant(id string) map[string]any {
	return map[string]any{
		"id":        id,
		"firstName": "Petra",
		"lastName":  "Akhras",
		"address": map[string]any{
			"street":        "Summergasse",
			"street_number": "3",
			"zip":           "3400",
			"city":          "Kierling",
		},
	}
}
