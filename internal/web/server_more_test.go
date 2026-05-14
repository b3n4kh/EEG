package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ben/eeg-sumsum/internal/db"
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
