package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ben/eeg-sumsum/internal/auth"
	"github.com/ben/eeg-sumsum/internal/db"
	"github.com/ben/eeg-sumsum/internal/eda"
	"github.com/xuri/excelize/v2"
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

func TestParticipantDashboardShowsSimplifiedSummary(t *testing.T) {
	ctx := context.Background()
	database := testDB(t)
	hash, err := auth.HashPassword("secret123")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CreateUser(ctx, "teilnehmer", "Teilnehmer", hash, db.RoleParticipant, true); err != nil {
		t.Fatal(err)
	}
	user, err := database.UserByUsername(ctx, "teilnehmer")
	if err != nil {
		t.Fatal(err)
	}
	for _, meter := range []db.MeteringPoint{
		{ID: "AT001", Direction: "CONSUMPTION"},
		{ID: "AT002", Direction: "CONSUMPTION"},
		{ID: "AT003", Direction: "CONSUMPTION"},
		{ID: "TOTAL", Direction: "CONSUMPTION"},
	} {
		if err := database.UpsertMeteringPoint(ctx, nil, meter); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.AssignMeters(ctx, user.ID, []string{"AT001", "AT002"}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(15 * time.Minute)
	batchID, err := database.UpsertImportBatch(ctx, nil, db.ImportBatch{
		Filename:    "report.xlsx",
		SHA256:      "participant-summary",
		ReportStart: &start,
		ReportEnd:   &end,
		DataStart:   &start,
		DataEnd:     &end,
	})
	if err != nil {
		t.Fatal(err)
	}
	measurements := []db.Measurement{
		{MeteringPointID: "AT001", Direction: "CONSUMPTION", MetricKey: db.MetricCommunityShareKey, MetricLabel: db.MetricCommunityShareLabel, IntervalStart: start, Value: 50},
		{MeteringPointID: "AT001", Direction: "CONSUMPTION", MetricKey: db.MetricTotalConsumptionKey, MetricLabel: db.MetricTotalConsumptionLabel, IntervalStart: start, Value: 200},
		{MeteringPointID: "AT002", Direction: "CONSUMPTION", MetricKey: db.MetricCommunityShareKey, MetricLabel: db.MetricCommunityShareLabel, IntervalStart: start, Value: 300},
		{MeteringPointID: "AT002", Direction: "CONSUMPTION", MetricKey: db.MetricTotalConsumptionKey, MetricLabel: db.MetricTotalConsumptionLabel, IntervalStart: start, Value: 600},
		{MeteringPointID: "AT003", Direction: "CONSUMPTION", MetricKey: db.MetricCommunityShareKey, MetricLabel: db.MetricCommunityShareLabel, IntervalStart: start, Value: 700},
		{MeteringPointID: "AT003", Direction: "CONSUMPTION", MetricKey: db.MetricTotalConsumptionKey, MetricLabel: db.MetricTotalConsumptionLabel, IntervalStart: start, Value: 700},
		{MeteringPointID: "TOTAL", Direction: "CONSUMPTION", MetricKey: db.MetricCommunityShareKey, MetricLabel: db.MetricCommunityShareLabel, IntervalStart: start, Value: 999},
		{MeteringPointID: "TOTAL", Direction: "CONSUMPTION", MetricKey: db.MetricTotalConsumptionKey, MetricLabel: db.MetricTotalConsumptionLabel, IntervalStart: start, Value: 999},
	}
	for _, measurement := range measurements {
		if _, err := database.UpsertMeasurement(ctx, nil, measurement, batchID); err != nil {
			t.Fatal(err)
		}
	}

	app := New(database, true)
	client, baseURL := testClient(app.Routes())
	login(t, client, baseURL, "teilnehmer", "secret123")

	resp, err := client.Get(baseURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)
	for _, want := range []string{
		db.MetricCommunityShareLabel,
		db.MetricTotalConsumptionLabel,
		"50.000 kWh",
		"200.000 kWh",
		"300.000 kWh",
		"600.000 kWh",
		"25.0%",
		"50.0%",
		"coverage-chart",
		`href="/meters/AT001"`,
		`href="/meters/AT002"`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("dashboard body does not contain %q: %s", want, page)
		}
	}
	for _, forbidden := range []string{"350.000 kWh", "700.000 kWh", "800.000 kWh", "999.000 kWh", "43.8%"} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("dashboard body contains forbidden aggregate %q: %s", forbidden, page)
		}
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

func TestAdminCanUploadXLSXFromUI(t *testing.T) {
	database := testDB(t)
	hash, err := auth.HashPassword("secret123")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CreateUser(context.Background(), "admin", "Admin", hash, db.RoleAdmin, true); err != nil {
		t.Fatal(err)
	}
	app := New(database, true)
	client, baseURL := testClient(app.Routes())
	login(t, client, baseURL, "admin", "secret123")

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "report.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(testWorkbook(t)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	resp, err := client.Post(baseURL+"/admin/imports", writer.FormDataContentType(), body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	metrics, err := database.MetricLabels(context.Background(), "AT001")
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 {
		t.Fatalf("metrics = %d, want 1", len(metrics))
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
		BaseURL:     edaServer.URL,
		Username:    "user@example.com",
		Password:    "secret",
		CommunityID: "community-1",
	})

	first := postEDAImport(t, app.Routes())
	if first.MeasurementsInserted != 13 || first.MeasurementsUpdated != 0 || first.MeasurementsSkipped != 0 {
		t.Fatalf("first summary = %+v, want 13 inserted", first)
	}
	second := postEDAImport(t, app.Routes())
	if second.MeasurementsInserted != 0 || second.MeasurementsUpdated != 0 || second.MeasurementsSkipped != 13 {
		t.Fatalf("second summary = %+v, want 13 skipped", second)
	}
	metrics, err := database.MetricLabels(context.Background(), "AT0010000000000000001000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 5 {
		t.Fatalf("metrics = %d, want 5", len(metrics))
	}
	users, err := database.Users(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		t.Fatalf("users = %d, want 1 imported participant", len(users))
	}
	if users[0].Username != "petra.akhras" || !users[0].PasswordChangeRequired {
		t.Fatalf("imported user = %+v, want petra.akhras with required password change", users[0])
	}
	assigned, err := database.AssignedMeterIDs(context.Background(), users[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(assigned) != 2 {
		t.Fatalf("assigned meters = %d, want 2", len(assigned))
	}
}

func TestParticipantMustChangeInitialPassword(t *testing.T) {
	database := testDB(t)
	hash, err := auth.HashPassword("secret12345")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CreateUser(context.Background(), "teilnehmer", "Teilnehmer", hash, db.RoleParticipant, true); err != nil {
		t.Fatal(err)
	}
	user, err := database.UserByUsername(context.Background(), "teilnehmer")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpdatePassword(context.Background(), user.ID, hash, true); err != nil {
		t.Fatal(err)
	}
	app := New(database, true)
	client, baseURL := testClient(app.Routes())
	login(t, client, baseURL, "teilnehmer", "secret12345")

	resp, err := client.Get(baseURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Passwort ändern") {
		t.Fatalf("GET / body does not contain password change form: %s", string(body))
	}

	form := url.Values{
		"current_password": {"secret12345"},
		"password":         {"newsecret12345"},
		"password_confirm": {"newsecret12345"},
	}
	resp, err = client.Post(baseURL+"/password/change", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Dashboard") {
		t.Fatalf("password change final body does not contain dashboard: %s", string(body))
	}
	updated, err := database.UserByUsername(context.Background(), "teilnehmer")
	if err != nil {
		t.Fatal(err)
	}
	if updated.PasswordChangeRequired {
		t.Fatal("password change flag is still set")
	}
}

func TestAdminCanImpersonateParticipantAndReturn(t *testing.T) {
	ctx := context.Background()
	database := testDB(t)
	hash, err := auth.HashPassword("secret12345")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CreateUser(ctx, "admin", "Admin", hash, db.RoleAdmin, true); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateUser(ctx, "teilnehmer", "Teilnehmer", hash, db.RoleParticipant, true); err != nil {
		t.Fatal(err)
	}
	participant, err := database.UserByUsername(ctx, "teilnehmer")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpdatePassword(ctx, participant.ID, hash, true); err != nil {
		t.Fatal(err)
	}
	for _, meter := range []db.MeteringPoint{
		{ID: "AT001", Direction: "CONSUMPTION"},
		{ID: "AT002", Direction: "CONSUMPTION"},
	} {
		if err := database.UpsertMeteringPoint(ctx, nil, meter); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.AssignMeters(ctx, participant.ID, []string{"AT001"}); err != nil {
		t.Fatal(err)
	}

	app := New(database, true)
	client, baseURL := testClient(app.Routes())
	login(t, client, baseURL, "admin", "secret12345")

	resp, err := client.Get(baseURL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), fmt.Sprintf(`/admin/users/%d/impersonate`, participant.ID)) {
		t.Fatalf("admin body does not contain impersonation form: %s", string(body))
	}

	resp, err = client.Post(baseURL+fmt.Sprintf("/admin/users/%d/impersonate", participant.ID), "application/x-www-form-urlencoded", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)
	for _, want := range []string{"Als Teilnehmer", "Zurück zu Admin", `href="/meters/AT001"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("impersonated dashboard does not contain %q: %s", want, page)
		}
	}
	for _, forbidden := range []string{"Passwort ändern", `href="/meters/AT002"`} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("impersonated dashboard contains forbidden %q: %s", forbidden, page)
		}
	}

	resp, err = client.Get(baseURL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("impersonated admin status = %d, want 403", resp.StatusCode)
	}

	resp, err = client.Post(baseURL+"/impersonation/stop", "application/x-www-form-urlencoded", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Administration") {
		t.Fatalf("stop impersonation body does not contain admin page: %s", string(body))
	}
}

func testWorkbook(t *testing.T) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	_, _ = f.NewSheet("Energiedaten")
	_, _ = f.NewSheet("Übersicht")
	_ = f.DeleteSheet("Sheet1")

	values := map[string]any{
		"Energiedaten!B2":  "AT001",
		"Energiedaten!B4":  "GENERATION",
		"Energiedaten!B5":  "01.05.2026 00:00",
		"Energiedaten!B6":  "01.05.2026 23:45",
		"Energiedaten!B7":  "01.05.2026 00:00",
		"Energiedaten!B8":  "01.05.2026 23:45",
		"Energiedaten!B14": "Wirkenergie [KWH]",
		"Energiedaten!A17": "01.05.2026 00:00",
		"Energiedaten!B17": "1,25",
		"Energiedaten!C17": "L1",
		"Übersicht!F6":     "Wirkenergie [KWH]",
		"Übersicht!A7":     "AT001",
		"Übersicht!B7":     "GENERATION",
		"Übersicht!C7":     "Operator",
		"Übersicht!D7":     "01.05.2026 00:00",
		"Übersicht!E7":     "01.05.2026 23:45",
		"Übersicht!F7":     "1,25",
		"Übersicht!O7":     "OK",
		"Übersicht!P7":     "L1",
	}
	for cell, value := range values {
		if err := f.SetCellValue(strings.Split(cell, "!")[0], strings.Split(cell, "!")[1], value); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
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
		case "/pwa/energycommunities/community-1":
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"meteringPoints": []map[string]any{
						{"meteringPointId": "AT0010000000000000001000000000001", "energyDirection": "CONSUMPTION", "participant": fakeParticipant("p-1")},
						{"meteringPointId": "AT0010000000000000001000000000002", "energyDirection": "GENERATION", "participant": fakeParticipant("p-2")},
					},
				},
			})
		case "/consumptionsurya/g/AT0010000000000000001000000000001":
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"s":       true,
				"data":    [][]any{{"2026-05-06T00:00:00", 10.0}},
				"meta":    map[string]any{"scale_x": "day"},
			})
		case "/consumptionsurya/p/AT0010000000000000001000000000001":
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"s":       true,
				"data":    [][]any{{"2026-05-06T00:00:00", 4.0}},
				"meta":    map[string]any{"scale_x": "day"},
			})
		case "/consumptionsurya/g/AT0010000000000000001000000000002":
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"s":       true,
				"data":    [][]any{{"2026-05-06T00:00:00", 20.0}},
				"meta":    map[string]any{"scale_x": "day"},
			})
		case "/consumptionsurya/p/AT0010000000000000001000000000002":
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
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
}

func fakeParticipant(id string) map[string]any {
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
