package testsupport

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/xuri/excelize/v2"

	"github.com/ben/eeg-sumsum/internal/auth"
	"github.com/ben/eeg-sumsum/internal/db"
)

type WorkbookMeter struct {
	ID              string
	Direction       string
	NetworkOperator string
	MetricLabel     string
	Values          []WorkbookValue
	SummaryValue    any
	Status          string
	Quality         string
}

type WorkbookValue struct {
	At      string
	Value   any
	Quality string
}

func DB(t testing.TB) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func Hash(t testing.TB, password string) string {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func CreateUser(t testing.TB, database *db.DB, username, displayName, password, role string, active bool) db.User {
	t.Helper()
	if err := database.CreateUser(context.Background(), username, displayName, Hash(t, password), role, active); err != nil {
		t.Fatal(err)
	}
	user, err := database.UserByUsername(context.Background(), username)
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func RequirePasswordChange(t testing.TB, database *db.DB, user db.User, password string, required bool) {
	t.Helper()
	if err := database.UpdatePassword(context.Background(), user.ID, Hash(t, password), required); err != nil {
		t.Fatal(err)
	}
}

func UpsertMeter(t testing.TB, database *db.DB, meter db.MeteringPoint) {
	t.Helper()
	if err := database.UpsertMeteringPoint(context.Background(), nil, meter); err != nil {
		t.Fatal(err)
	}
}

func AssignMeters(t testing.TB, database *db.DB, user db.User, ids ...string) {
	t.Helper()
	if err := database.AssignMeters(context.Background(), user.ID, ids); err != nil {
		t.Fatal(err)
	}
}

func SeedBatch(t testing.TB, database *db.DB, sha string) int64 {
	t.Helper()
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(15 * time.Minute)
	id, err := database.UpsertImportBatch(context.Background(), nil, db.ImportBatch{
		Filename:    sha + ".xlsx",
		SHA256:      sha,
		ReportStart: &start,
		ReportEnd:   &end,
		DataStart:   &start,
		DataEnd:     &end,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func AddMeasurement(t testing.TB, database *db.DB, batchID int64, meterID, direction, metricKey, metricLabel string, at time.Time, value float64) {
	t.Helper()
	if _, err := database.UpsertMeasurement(context.Background(), nil, db.Measurement{
		MeteringPointID: meterID,
		Direction:       direction,
		MetricKey:       metricKey,
		MetricLabel:     metricLabel,
		IntervalStart:   at,
		Value:           value,
	}, batchID); err != nil {
		t.Fatal(err)
	}
}

func HTTPClient(t testing.TB, handler http.Handler) (*http.Client, string) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := server.Client()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client.Jar = jar
	return client, server.URL
}

func Login(t testing.TB, client *http.Client, baseURL, username, password string) string {
	t.Helper()
	form := url.Values{"username": {username}, "password": {password}}
	resp, err := client.Post(baseURL+"/login", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login final status = %d, body = %s", resp.StatusCode, body)
	}
	return string(body)
}

func PostXLSX(t testing.TB, client *http.Client, endpoint, filename string, content []byte) (*http.Response, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	resp, err := client.Post(endpoint, writer.FormDataContentType(), body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(raw)
}

func DecodeJSON[T any](t testing.TB, body io.Reader) T {
	t.Helper()
	var value T
	if err := json.NewDecoder(body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func Workbook(t testing.TB, meters []WorkbookMeter) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	_, _ = f.NewSheet("Energiedaten")
	_, _ = f.NewSheet("Übersicht")
	_ = f.DeleteSheet("Sheet1")

	reportStart := "01.05.2026 00:00"
	reportEnd := "01.05.2026 23:45"
	headerMetricCol, _ := excelize.ColumnNumberToName(6)
	if err := f.SetCellValue("Übersicht", headerMetricCol+"6", defaultMetricLabel(meters)); err != nil {
		t.Fatal(err)
	}
	for i, meter := range meters {
		if meter.ID == "" {
			meter.ID = "AT00" + strings.Repeat("1", 29)
		}
		if meter.Direction == "" {
			meter.Direction = "CONSUMPTION"
		}
		if meter.MetricLabel == "" {
			meter.MetricLabel = "Wirkenergie [KWH]"
		}
		if len(meter.Values) == 0 {
			meter.Values = []WorkbookValue{{At: reportStart, Value: "1,25", Quality: "L1"}}
		}
		valueCol := 2 + i*2
		qualityCol := valueCol + 1
		setCell(t, f, "Energiedaten", 2, valueCol, meter.ID)
		setCell(t, f, "Energiedaten", 4, valueCol, meter.Direction)
		setCell(t, f, "Energiedaten", 5, valueCol, reportStart)
		setCell(t, f, "Energiedaten", 6, valueCol, reportEnd)
		setCell(t, f, "Energiedaten", 7, valueCol, reportStart)
		setCell(t, f, "Energiedaten", 8, valueCol, reportEnd)
		setCell(t, f, "Energiedaten", 14, valueCol, meter.MetricLabel)
		for rowOffset, value := range meter.Values {
			row := 17 + rowOffset
			if value.At != "" {
				setCell(t, f, "Energiedaten", row, 1, value.At)
			}
			setCell(t, f, "Energiedaten", row, valueCol, value.Value)
			setCell(t, f, "Energiedaten", row, qualityCol, value.Quality)
		}

		overviewRow := 7 + i
		setCell(t, f, "Übersicht", overviewRow, 1, meter.ID)
		setCell(t, f, "Übersicht", overviewRow, 2, meter.Direction)
		setCell(t, f, "Übersicht", overviewRow, 3, valueOr(meter.NetworkOperator, "Operator"))
		setCell(t, f, "Übersicht", overviewRow, 4, reportStart)
		setCell(t, f, "Übersicht", overviewRow, 5, reportEnd)
		setCell(t, f, "Übersicht", overviewRow, 6, valueOrAny(meter.SummaryValue, "1,25"))
		setCell(t, f, "Übersicht", overviewRow, 15, valueOr(meter.Status, "OK"))
		setCell(t, f, "Übersicht", overviewRow, 16, valueOr(meter.Quality, "L1"))
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func setCell(t testing.TB, f *excelize.File, sheet string, row, col int, value any) {
	t.Helper()
	name, err := excelize.CoordinatesToCellName(col, row)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.SetCellValue(sheet, name, value); err != nil {
		t.Fatal(err)
	}
}

func defaultMetricLabel(meters []WorkbookMeter) string {
	for _, meter := range meters {
		if meter.MetricLabel != "" {
			return meter.MetricLabel
		}
	}
	return "Wirkenergie [KWH]"
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func valueOrAny(value any, fallback any) any {
	if value == nil {
		return fallback
	}
	return value
}
