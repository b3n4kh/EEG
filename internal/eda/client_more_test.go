package eda

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ben/eeg-sumsum/internal/db"
)

func TestConfiguredMeteringPointsParsesDirectionsAndOperators(t *testing.T) {
	points := configuredMeteringPoints(" AT0010000000000000001000000000001:verbrauch:OP1, AT0010000000000000001000000000002:erzeugung:OP2, invalid ")
	require.Len(t, points, 2)
	require.Equal(t, meterPoint{ID: "AT0010000000000000001000000000001", Direction: directionConsumption, NetworkOperator: "OP1"}, points[0])
	require.Equal(t, meterPoint{ID: "AT0010000000000000001000000000002", Direction: directionGeneration, NetworkOperator: "OP2"}, points[1])
}

func TestParticipantAccountsFromCommunityDedupesAndSortsMeters(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"data": map[string]any{
			"meteringPoints": []map[string]any{
				{"meteringPointId": "AT0010000000000000001000000000002", "participant": richParticipant("Petra", "Äkhras", "3")},
				{"meteringPointId": "AT0010000000000000001000000000001", "participant": richParticipant("Petra", "Äkhras", "3")},
				{"meteringPointId": "AT0010000000000000001000000000003", "participant": richParticipant("Zoe", "Müller", "7")},
				{"meteringPointId": "AT00100000000RC107893000000001178", "participant": richParticipant("Ignored", "Person", "9")},
			},
		},
	})
	require.NoError(t, err)

	accounts := participantAccountsFromCommunity(raw)
	require.Len(t, accounts, 2)
	require.Equal(t, "Petra Äkhras", accounts[0].DisplayName)
	require.Equal(t, "petra.aekhras", accounts[0].Username)
	require.Equal(t, []string{"AT0010000000000000001000000000001", "AT0010000000000000001000000000002"}, accounts[0].MeteringPointIDs)
	require.Equal(t, "zoe.mueller", accounts[1].Username)
}

func TestSeriesDerivationsUseMinSeriesAndTotals(t *testing.T) {
	at := time.Date(2026, 5, 1, 0, 0, 0, 0, viennaLocation)
	gSeries := []seriesPoint{{IntervalStart: at, Value: 10}, {IntervalStart: at.Add(24 * time.Hour), Value: 5}}
	pSeries := []seriesPoint{{IntervalStart: at, Value: 4}, {IntervalStart: at.Add(24 * time.Hour), Value: 7}}

	own := minSeries(gSeries, pSeries)
	require.Equal(t, []seriesPoint{{IntervalStart: at, Value: 4}, {IntervalStart: at.Add(24 * time.Hour), Value: 5}}, own)
	require.Equal(t, 15.0, sumSeries(gSeries))

	measurements := pointMeasurements(meterPoint{ID: "AT001", Direction: directionConsumption}, gSeries, pSeries)
	require.Len(t, measurements, 10)
	require.Equal(t, db.MetricTotalConsumptionKey, measurements[0].MetricKey)
	totals := totalMeasurements(measurements)
	require.NotEmpty(t, totals)
	require.Equal(t, "TOTAL", totals[0].MeteringPointID)
}

func TestPostRawReturnsHTTPErrorBodySnippet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad upstream", http.StatusBadGateway)
	}))
	defer server.Close()

	_, err := postRaw(context.Background(), server.Client(), server.URL, "token", map[string]string{"hello": "world"})
	require.ErrorContains(t, err, "HTTP 502")
	require.ErrorContains(t, err, "bad upstream")
}

func richParticipant(first, last, number string) map[string]any {
	return map[string]any{
		"id":        first + "-" + last,
		"firstName": first,
		"lastName":  last,
		"address": map[string]any{
			"street":        "Summergasse",
			"street_number": number,
			"zip":           "3400",
			"city":          "Kierling",
		},
	}
}
