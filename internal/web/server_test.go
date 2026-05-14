package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben/eeg-sumsum/internal/auth"
	"github.com/ben/eeg-sumsum/internal/db"
	"github.com/ben/eeg-sumsum/internal/eda"
)

func TestParticipantCannotAccessAdmin(t *testing.T) {
	database := testDB(t)
	hash, err := auth.HashPassword("secret123")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CreateUser(context.Background(), "teilnehmer", "Teilnehmer", hash, db.RoleParticipant, true); err != nil {
		t.Fatal(err)
	}
	app := New(database, true)
	client, baseURL := testClient(app.Routes())
	login(t, client, baseURL, "teilnehmer", "secret123")

	resp, err := client.Get(baseURL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestParticipantCannotAccessUnassignedMeter(t *testing.T) {
	database := testDB(t)
	hash, err := auth.HashPassword("secret123")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CreateUser(context.Background(), "teilnehmer", "Teilnehmer", hash, db.RoleParticipant, true); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertMeteringPoint(context.Background(), nil, db.MeteringPoint{ID: "AT001", Direction: "CONSUMPTION"}); err != nil {
		t.Fatal(err)
	}
	app := New(database, true)
	client, baseURL := testClient(app.Routes())
	login(t, client, baseURL, "teilnehmer", "secret123")

	resp, err := client.Get(baseURL + "/meters/AT001")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestUploadAPIRejectsInvalidToken(t *testing.T) {
	database := testDB(t)
	app := New(database, true)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/imports", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestEDAImportAPIImportsIdempotently(t *testing.T) {
	database := testDB(t)
	if err := (auth.Service{DB: database}).BootstrapAPIToken(context.Background(), "dev-token"); err != nil {
		t.Fatal(err)
	}
	edaServer := fakeEDAServer(t)
	defer edaServer.Close()
	app := New(database, true, eda.Config{
		BaseURL:           edaServer.URL,
		Username:          "user@example.com",
		Password:          "secret",
		CommunityID:       "community-1",
		MeteringPointID:   "EDA_TEST",
		MeteringPointName: "EDA Test",
	})

	first := postEDAImport(t, app.Routes())
	if first.MeasurementsInserted != 2 || first.MeasurementsUpdated != 0 || first.MeasurementsSkipped != 0 {
		t.Fatalf("first summary = %+v, want 2 inserted", first)
	}
	second := postEDAImport(t, app.Routes())
	if second.MeasurementsInserted != 0 || second.MeasurementsUpdated != 0 || second.MeasurementsSkipped != 2 {
		t.Fatalf("second summary = %+v, want 2 skipped", second)
	}
	metrics, err := database.MetricLabels(context.Background(), "EDA_TEST")
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 2 {
		t.Fatalf("metrics = %d, want 2", len(metrics))
	}
}

func testDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func fakeEDAServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v4/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "test-token"})
		case "/pwa/energycommunities/community-1/kpiData":
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"autarky":          27.8,
					"ownConsumption":   34.5,
					"community":        107.4,
					"feed":             203.5,
					"remainingDemand":  279.0,
					"communityGrouped": []map[string]any{{"enixiGenerationType": "Photovoltaik", "sum": 107.4}},
				},
			})
		case "/pwa/energycommunities/community-1/meterdata":
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"s":       true,
				"data": map[string]any{
					"substitutesOrMissingData": false,
					"sumGeneration":            12.5,
					"sumFeed":                  7.0,
					"generationSeries":         []map[string]any{{"date": "2026-05-06T00:00:00", "value": 12.5, "methods": "L1"}},
					"feedSeries":               []map[string]any{{"date": "2026-05-06T00:00:00", "value": 7.0, "methods": nil}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func postEDAImport(t *testing.T, handler http.Handler) db.ImportSummary {
	t.Helper()
	body := bytes.NewBufferString(`{"from":"2026-05-06","to":"2026-05-06"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/eda-imports", body)
	req.Header.Set("Authorization", "Bearer dev-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var summary db.ImportSummary
	if err := json.NewDecoder(rec.Body).Decode(&summary); err != nil {
		t.Fatal(err)
	}
	return summary
}

func testClient(handler http.Handler) (*http.Client, string) {
	server := httptest.NewServer(handler)
	client := server.Client()
	jar, _ := cookiejar.New(nil)
	client.Jar = jar
	return client, server.URL
}

func login(t *testing.T, client *http.Client, baseURL, username, password string) {
	t.Helper()
	form := url.Values{"username": {username}, "password": {password}}
	resp, err := client.Post(baseURL+"/login", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login final status = %d, want 200", resp.StatusCode)
	}
}
