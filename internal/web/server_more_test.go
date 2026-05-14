package web

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ben/eeg-sumsum/internal/db"
	"github.com/ben/eeg-sumsum/internal/eda"
	"github.com/ben/eeg-sumsum/internal/testsupport"
)

func TestParticipantCannotAccessTotalMeterEvenIfAssigned(t *testing.T) {
	database := testsupport.DB(t)
	participant := testsupport.CreateUser(t, database, "teilnehmer-total", "Teilnehmer", "secret12345", db.RoleParticipant, true)
	testsupport.UpsertMeter(t, database, db.MeteringPoint{ID: "TOTAL", Direction: "CONSUMPTION"})
	testsupport.AssignMeters(t, database, participant, "TOTAL")

	app := New(database, true)
	client, baseURL := testsupport.HTTPClient(t, app.Routes())
	testsupport.Login(t, client, baseURL, "teilnehmer-total", "secret12345")

	resp, err := client.Get(baseURL + "/meters/TOTAL")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestEDARangeAcceptsQueryFormAndJSON(t *testing.T) {
	formReq, err := http.NewRequest(http.MethodPost, "/api/admin/eda-imports?from=2026-05-01&to=2026-05-02", nil)
	require.NoError(t, err)
	from, to, err := edaRange(formReq)
	require.NoError(t, err)
	require.Equal(t, "2026-04-30T22:00:00Z", from.UTC().Format("2006-01-02T15:04:05Z"))
	require.Equal(t, "2026-05-02T21:45:00Z", to.UTC().Format("2006-01-02T15:04:05Z"))

	jsonReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "/api/admin/eda-imports", strings.NewReader(`{"from":"2026-05-01T01:15","to":"2026-05-01T02:30"}`))
	require.NoError(t, err)
	jsonReq.Header.Set("Content-Type", "application/json")
	from, to, err = edaRange(jsonReq)
	require.NoError(t, err)
	require.Equal(t, "2026-04-30T23:15:00Z", from.UTC().Format("2006-01-02T15:04:05Z"))
	require.Equal(t, "2026-05-01T00:30:00Z", to.UTC().Format("2006-01-02T15:04:05Z"))
}

func TestEDAAutoImportRangeUsesLastThirtyCompletedDays(t *testing.T) {
	loc := viennaLocation()
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, loc)
	from, to := edaAutoImportRange(now, 30, loc)

	require.Equal(t, "2026-04-14 00:00", from.In(loc).Format("2006-01-02 15:04"))
	require.Equal(t, "2026-05-13 23:45", to.In(loc).Format("2006-01-02 15:04"))
}

func TestScheduledEDAImportStoresStatus(t *testing.T) {
	ctx := context.Background()
	database := testsupport.DB(t)
	edaServer := fakeEDAServer(t)
	defer edaServer.Close()
	app := New(database, true, eda.Config{
		BaseURL:     edaServer.URL,
		Username:    "user@example.com",
		Password:    "secret",
		CommunityID: "community-1",
	})
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, viennaLocation())

	require.NoError(t, app.runScheduledEDAImport(ctx, now))

	status, err := database.ScheduledImportStatus(ctx, autoEDAImportJobName)
	require.NoError(t, err)
	require.Equal(t, now.UTC(), *status.LastStartedAt)
	require.NotNil(t, status.LastFinishedAt)
	require.NotNil(t, status.LastSuccess)
	require.True(t, *status.LastSuccess)
	require.Contains(t, status.LastResult, "13 gelesen")
	require.Contains(t, status.LastResult, "13 neu")
	require.Empty(t, status.LastError)
}

func TestAdminShowsEDAAutoImportStatus(t *testing.T) {
	ctx := context.Background()
	database := testsupport.DB(t)
	testsupport.CreateUser(t, database, "admin-auto", "Admin", "secret12345", db.RoleAdmin, true)
	started := time.Date(2026, 5, 14, 1, 0, 0, 0, time.UTC)
	finished := started.Add(2 * time.Minute)
	require.NoError(t, database.RecordScheduledImportFinished(ctx, autoEDAImportJobName, started, finished, true, "13 gelesen, 13 neu, 0 aktualisiert, 0 unverändert", ""))
	app := New(database, true, eda.Config{
		Username:    "eda-user",
		Password:    "eda-password",
		CommunityID: "community-1",
	})
	require.NoError(t, app.StartEDAAutoImport(ctx, EDAAutoImportConfig{Enabled: true, Schedule: "15 3 * * *"}))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, app.StopEDAAutoImport(stopCtx))
	})
	client, baseURL := testsupport.HTTPClient(t, app.Routes())
	testsupport.Login(t, client, baseURL, "admin-auto", "secret12345")

	resp, err := client.Get(baseURL + "/admin")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	require.NoError(t, err)
	page := string(body)
	for _, want := range []string{
		"Automatik",
		"aktiv",
		"Letzter Aufruf",
		"14.05.2026 03:00",
		"Letztes Ergebnis",
		"13 gelesen, 13 neu",
		"Nächster Lauf",
		"EDA importieren",
	} {
		require.Contains(t, page, want)
	}
}
