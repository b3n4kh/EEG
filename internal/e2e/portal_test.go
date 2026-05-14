package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/stretchr/testify/require"

	"github.com/ben/eeg-sumsum/internal/auth"
	"github.com/ben/eeg-sumsum/internal/db"
	"github.com/ben/eeg-sumsum/internal/eda"
	"github.com/ben/eeg-sumsum/internal/testsupport"
	"github.com/ben/eeg-sumsum/internal/web"
)

func TestFeatures(t *testing.T) {
	world := &portalWorld{t: t}
	suite := godog.TestSuite{
		Name:                "portal",
		ScenarioInitializer: world.InitializeScenario,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"features"},
			Strict:   true,
			TestingT: t,
		},
	}
	require.Zero(t, suite.Run())
}

type portalWorld struct {
	t             *testing.T
	database      *db.DB
	client        *http.Client
	baseURL       string
	edaServer     *httptest.Server
	edaConfig     eda.Config
	lastStatus    int
	lastBody      string
	firstSummary  db.ImportSummary
	secondSummary db.ImportSummary
}

func (w *portalWorld) InitializeScenario(ctx *godog.ScenarioContext) {
	ctx.Step(`^a fresh portal$`, w.aFreshPortal)
	ctx.Step(`^a fresh portal with a fake EDA API$`, w.aFreshPortalWithFakeEDAAPI)
	ctx.Step(`^an admin user "([^"]*)" with password "([^"]*)"$`, w.anAdminUserWithPassword)
	ctx.Step(`^participant "([^"]*)" with password "([^"]*)" is assigned to "([^"]*)"$`, w.participantWithPasswordIsAssignedTo)
	ctx.Step(`^participant dashboard data exists for assigned, unassigned, and total meters$`, w.participantDashboardDataExists)
	ctx.Step(`^an API token "([^"]*)"$`, w.anAPIToken)
	ctx.Step(`^I log in as "([^"]*)" with password "([^"]*)"$`, w.iLogInAsWithPassword)
	ctx.Step(`^I upload a workbook through the admin UI$`, w.iUploadAWorkbookThroughTheAdminUI)
	ctx.Step(`^I upload the same workbook through the import API twice$`, w.iUploadTheSameWorkbookThroughTheImportAPITwice)
	ctx.Step(`^I import EDA data through the API twice$`, w.iImportEDADataThroughTheAPITwice)
	ctx.Step(`^I open "([^"]*)"$`, w.iOpen)
	ctx.Step(`^I change the password from "([^"]*)" to "([^"]*)"$`, w.iChangeThePassword)
	ctx.Step(`^I set EDA participant "([^"]*)" password to "([^"]*)"$`, w.iSetEDAParticipantPasswordTo)
	ctx.Step(`^the response contains "([^"]*)"$`, w.theResponseContains)
	ctx.Step(`^the response does not contain "([^"]*)"$`, w.theResponseDoesNotContain)
	ctx.Step(`^the response status is (\d+)$`, w.theResponseStatusIs)
	ctx.Step(`^meter "([^"]*)" has (\d+) metric label$`, w.meterHasMetricLabel)
	ctx.Step(`^the first API import inserted (\d+) measurements$`, w.theFirstAPIImportInsertedMeasurements)
	ctx.Step(`^the second API import skipped (\d+) measurements$`, w.theSecondAPIImportSkippedMeasurements)
	ctx.Step(`^the first EDA import inserted (\d+) measurements$`, w.theFirstAPIImportInsertedMeasurements)
	ctx.Step(`^the second EDA import skipped (\d+) measurements$`, w.theSecondAPIImportSkippedMeasurements)
	ctx.Step(`^EDA participant "([^"]*)" has (\d+) assigned meters and must change password$`, w.edaParticipantHasAssignedMetersAndMustChangePassword)
}

func (w *portalWorld) aFreshPortal() error {
	if w.edaServer != nil {
		w.edaServer.Close()
	}
	w.database = testsupport.DB(w.t)
	w.client = nil
	w.baseURL = ""
	w.edaConfig = eda.Config{}
	w.lastStatus = 0
	w.lastBody = ""
	w.firstSummary = db.ImportSummary{}
	w.secondSummary = db.ImportSummary{}
	return nil
}

func (w *portalWorld) aFreshPortalWithFakeEDAAPI() error {
	if err := w.aFreshPortal(); err != nil {
		return err
	}
	w.edaServer = fakeEDAServer()
	w.t.Cleanup(w.edaServer.Close)
	w.edaConfig = eda.Config{
		BaseURL:     w.edaServer.URL,
		Username:    "user@example.com",
		Password:    "secret",
		CommunityID: "community-1",
	}
	return nil
}

func (w *portalWorld) anAdminUserWithPassword(username, password string) error {
	testsupport.CreateUser(w.t, w.database, username, "Admin", password, db.RoleAdmin, true)
	return nil
}

func (w *portalWorld) participantWithPasswordIsAssignedTo(username, password, meterID string) error {
	user := testsupport.CreateUser(w.t, w.database, username, "Teilnehmer", password, db.RoleParticipant, true)
	for _, id := range []string{meterID, "AT002", "TOTAL"} {
		testsupport.UpsertMeter(w.t, w.database, db.MeteringPoint{ID: id, Direction: "CONSUMPTION"})
	}
	testsupport.AssignMeters(w.t, w.database, user, meterID, "TOTAL")
	return nil
}

func (w *portalWorld) participantDashboardDataExists() error {
	batchID := testsupport.SeedBatch(w.t, w.database, "participant-e2e")
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	for _, m := range []struct {
		meterID string
		key     string
		label   string
		value   float64
	}{
		{"AT001", db.MetricCommunityShareKey, db.MetricCommunityShareLabel, 50},
		{"AT001", db.MetricTotalConsumptionKey, db.MetricTotalConsumptionLabel, 200},
		{"AT002", db.MetricCommunityShareKey, db.MetricCommunityShareLabel, 700},
		{"TOTAL", db.MetricCommunityShareKey, db.MetricCommunityShareLabel, 999},
	} {
		testsupport.AddMeasurement(w.t, w.database, batchID, m.meterID, "CONSUMPTION", m.key, m.label, start, m.value)
	}
	return nil
}

func (w *portalWorld) anAPIToken(token string) error {
	return (auth.Service{DB: w.database}).BootstrapAPIToken(context.Background(), token)
}

func (w *portalWorld) iLogInAsWithPassword(username, password string) error {
	w.ensureApp()
	form := url.Values{"username": {username}, "password": {password}}
	resp, err := w.client.Post(w.baseURL+"/login", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	return w.capture(resp)
}

func (w *portalWorld) iUploadAWorkbookThroughTheAdminUI() error {
	w.ensureApp()
	resp, body := testsupport.PostXLSX(w.t, w.client, w.baseURL+"/admin/imports", "report.xlsx", e2eWorkbook(w.t))
	w.lastStatus = resp.StatusCode
	w.lastBody = body
	return nil
}

func (w *portalWorld) iUploadTheSameWorkbookThroughTheImportAPITwice() error {
	w.ensureApp()
	content := e2eWorkbook(w.t)
	first, err := w.postImportAPI(content)
	if err != nil {
		return err
	}
	second, err := w.postImportAPI(content)
	if err != nil {
		return err
	}
	w.firstSummary = first
	w.secondSummary = second
	return nil
}

func (w *portalWorld) iImportEDADataThroughTheAPITwice() error {
	w.ensureApp()
	first, err := w.postEDAImportAPI()
	if err != nil {
		return err
	}
	second, err := w.postEDAImportAPI()
	if err != nil {
		return err
	}
	w.firstSummary = first
	w.secondSummary = second
	return nil
}

func (w *portalWorld) iOpen(path string) error {
	w.ensureApp()
	resp, err := w.client.Get(w.baseURL + path)
	if err != nil {
		return err
	}
	return w.capture(resp)
}

func (w *portalWorld) iChangeThePassword(current, next string) error {
	w.ensureApp()
	form := url.Values{
		"current_password": {current},
		"password":         {next},
		"password_confirm": {next},
	}
	resp, err := w.client.Post(w.baseURL+"/password/change", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	return w.capture(resp)
}

func (w *portalWorld) iSetEDAParticipantPasswordTo(username, password string) error {
	user, err := w.database.UserByUsername(context.Background(), username)
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	return w.database.UpdatePassword(context.Background(), user.ID, hash, true)
}

func (w *portalWorld) theResponseContains(text string) error {
	if !strings.Contains(w.lastBody, text) {
		return fmt.Errorf("response does not contain %q; status=%d body=%s", text, w.lastStatus, w.lastBody)
	}
	return nil
}

func (w *portalWorld) theResponseDoesNotContain(text string) error {
	if strings.Contains(w.lastBody, text) {
		return fmt.Errorf("response contains forbidden %q; status=%d body=%s", text, w.lastStatus, w.lastBody)
	}
	return nil
}

func (w *portalWorld) theResponseStatusIs(status int) error {
	if w.lastStatus != status {
		return fmt.Errorf("response status = %d, want %d; body=%s", w.lastStatus, status, w.lastBody)
	}
	return nil
}

func (w *portalWorld) meterHasMetricLabel(meterID string, want int) error {
	metrics, err := w.database.MetricLabels(context.Background(), meterID)
	if err != nil {
		return err
	}
	if len(metrics) != want {
		return fmt.Errorf("meter %s metric labels = %d, want %d", meterID, len(metrics), want)
	}
	return nil
}

func (w *portalWorld) theFirstAPIImportInsertedMeasurements(want int) error {
	if w.firstSummary.MeasurementsInserted != want {
		return fmt.Errorf("first import inserted %d, want %d: %+v", w.firstSummary.MeasurementsInserted, want, w.firstSummary)
	}
	return nil
}

func (w *portalWorld) theSecondAPIImportSkippedMeasurements(want int) error {
	if w.secondSummary.MeasurementsSkipped != want {
		return fmt.Errorf("second import skipped %d, want %d: %+v", w.secondSummary.MeasurementsSkipped, want, w.secondSummary)
	}
	return nil
}

func (w *portalWorld) edaParticipantHasAssignedMetersAndMustChangePassword(username string, want int) error {
	user, err := w.database.UserByUsername(context.Background(), username)
	if err != nil {
		return err
	}
	if !user.PasswordChangeRequired {
		return fmt.Errorf("user %s is not marked for password change", username)
	}
	assigned, err := w.database.AssignedMeterIDs(context.Background(), user.ID)
	if err != nil {
		return err
	}
	if len(assigned) != want {
		return fmt.Errorf("assigned meters = %d, want %d", len(assigned), want)
	}
	return nil
}

func (w *portalWorld) ensureApp() {
	if w.client != nil {
		return
	}
	app := web.New(w.database, true, w.edaConfig)
	w.client, w.baseURL = testsupport.HTTPClient(w.t, app.Routes())
}

func (w *portalWorld) capture(resp *http.Response) error {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	w.lastStatus = resp.StatusCode
	w.lastBody = string(body)
	return nil
}

func (w *portalWorld) postImportAPI(content []byte) (db.ImportSummary, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "report.xlsx")
	if err != nil {
		return db.ImportSummary{}, err
	}
	if _, err := part.Write(content); err != nil {
		return db.ImportSummary{}, err
	}
	if err := writer.Close(); err != nil {
		return db.ImportSummary{}, err
	}
	req, err := http.NewRequest(http.MethodPost, w.baseURL+"/api/admin/imports", body)
	if err != nil {
		return db.ImportSummary{}, err
	}
	req.Header.Set("Authorization", "Bearer dev-token")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := w.client.Do(req)
	if err != nil {
		return db.ImportSummary{}, err
	}
	defer resp.Body.Close()
	w.lastStatus = resp.StatusCode
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return db.ImportSummary{}, err
	}
	w.lastBody = string(raw)
	if resp.StatusCode != http.StatusOK {
		return db.ImportSummary{}, fmt.Errorf("import API status = %d body=%s", resp.StatusCode, raw)
	}
	var summary db.ImportSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		return db.ImportSummary{}, err
	}
	return summary, nil
}

func (w *portalWorld) postEDAImportAPI() (db.ImportSummary, error) {
	req, err := http.NewRequest(http.MethodPost, w.baseURL+"/api/admin/eda-imports", strings.NewReader(`{"from":"2026-05-06","to":"2026-05-06"}`))
	if err != nil {
		return db.ImportSummary{}, err
	}
	req.Header.Set("Authorization", "Bearer dev-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return db.ImportSummary{}, err
	}
	defer resp.Body.Close()
	w.lastStatus = resp.StatusCode
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return db.ImportSummary{}, err
	}
	w.lastBody = string(raw)
	if resp.StatusCode != http.StatusOK {
		return db.ImportSummary{}, fmt.Errorf("EDA import API status = %d body=%s", resp.StatusCode, raw)
	}
	var summary db.ImportSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		return db.ImportSummary{}, err
	}
	return summary, nil
}

func e2eWorkbook(t testing.TB) []byte {
	t.Helper()
	return testsupport.Workbook(t, []testsupport.WorkbookMeter{{
		ID:          "AT001",
		Direction:   "GENERATION",
		MetricLabel: "Wirkenergie [KWH]",
		Values:      []testsupport.WorkbookValue{{At: "01.05.2026 00:00", Value: "1,25", Quality: "L1"}},
	}})
}

func fakeEDAServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v4/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "test-token"})
		case "/pwa/energycommunities/community-1":
			if r.Header.Get("Authorization") != "Bearer test-token" {
				http.Error(w, "missing bearer", http.StatusUnauthorized)
				return
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
			_ = json.NewEncoder(w).Encode(seriesResponse(10.0))
		case "/consumptionsurya/p/AT0010000000000000001000000000001":
			_ = json.NewEncoder(w).Encode(seriesResponse(4.0))
		case "/consumptionsurya/g/AT0010000000000000001000000000002":
			_ = json.NewEncoder(w).Encode(seriesResponse(20.0))
		case "/consumptionsurya/p/AT0010000000000000001000000000002":
			_ = json.NewEncoder(w).Encode(seriesResponse(7.0))
		default:
			http.NotFound(w, r)
		}
	}))
}

func seriesResponse(value float64) map[string]any {
	return map[string]any{
		"success": true,
		"s":       true,
		"data":    [][]any{{"2026-05-06T00:00:00", value}},
		"meta":    map[string]any{"scale_x": "day"},
	}
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
