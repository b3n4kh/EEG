package imports

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ben/eeg-sumsum/internal/db"
	"github.com/ben/eeg-sumsum/internal/testsupport"
)

func TestMetricKeyNormalizesGermanLabels(t *testing.T) {
	tests := map[string]string{
		"Anteil gemeinschaftliche Erzeugung [KWH]": "anteil_gemeinschaftliche_erzeugung_kwh",
		"Gesamt/Überschusserzeugung, Größe":        "gesamt_ueberschusserzeugung_groesse",
		"  äöü ß é !!! ":                           "aeoeue_ss_e",
		"!!!":                                      "unknown",
	}
	for label, want := range tests {
		require.Equal(t, want, MetricKey(label))
	}
}

func TestParseTimeUsesViennaAndSupportsSeconds(t *testing.T) {
	parsed, ok := parseTime("01.05.2026 01:30:05")
	require.True(t, ok)
	require.Equal(t, "2026-04-30T23:30:05Z", parsed.UTC().Format(time.RFC3339))

	parsed, ok = parseTime("01.01.2026 01:30")
	require.True(t, ok)
	require.Equal(t, "2026-01-01T00:30:00Z", parsed.UTC().Format(time.RFC3339))

	_, ok = parseTime("2026-01-01")
	require.False(t, ok)
}

func TestParseGeneratedWorkbookHandlesMultipleMetersAndSkipsMalformedRows(t *testing.T) {
	content := testsupport.Workbook(t, []testsupport.WorkbookMeter{
		{
			ID:              "AT001",
			Direction:       "CONSUMPTION",
			NetworkOperator: "ATNET001",
			MetricLabel:     "Wirkenergie [KWH]",
			Values: []testsupport.WorkbookValue{
				{At: "01.05.2026 00:00", Value: "1,25", Quality: "L1"},
				{At: "01.05.2026 00:15", Value: "not-a-number", Quality: "BAD"},
				{At: "not-a-date", Value: "2,5", Quality: "BAD"},
			},
			Status:  "Vollständig",
			Quality: "Q1",
		},
		{
			ID:              "AT002",
			Direction:       "GENERATION",
			NetworkOperator: "ATNET002",
			MetricLabel:     "Erzeugung [KWH]",
			Values:          []testsupport.WorkbookValue{{At: "01.05.2026 00:00", Value: "3,75", Quality: "L2"}},
			SummaryValue:    "3,75",
			Status:          "Teilweise",
			Quality:         "Q2",
		},
	})

	parsed, err := Parse("report.xlsx", content)
	require.NoError(t, err)
	require.Len(t, parsed.Meters, 2)
	require.Equal(t, "ATNET001", parsed.Meters["AT001"].NetworkOperator)
	require.Equal(t, "GENERATION", parsed.Meters["AT002"].Direction)
	require.Len(t, parsed.Measurements, 2)
	require.Len(t, parsed.Summaries, 2)
	require.Equal(t, "Vollständig", parsed.Summaries[0].Status)
	require.Equal(t, "Q1", parsed.Summaries[0].Quality)
	require.NotNil(t, parsed.ReportStart)
	require.NotNil(t, parsed.DataEnd)
}

func TestImportParsedUpdatesOverlappingMeasurementFromNewBatch(t *testing.T) {
	database := testsupport.DB(t)
	importer := Importer{DB: database}
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	first := parsedFixture("sha-1", 1.25, start)
	second := parsedFixture("sha-2", 2.50, start)

	firstSummary, err := importer.ImportParsed(context.Background(), first, nil)
	require.NoError(t, err)
	require.Equal(t, 1, firstSummary.MeasurementsInserted)
	require.Equal(t, 0, firstSummary.MeasurementsUpdated)

	secondSummary, err := importer.ImportParsed(context.Background(), second, nil)
	require.NoError(t, err)
	require.Equal(t, 0, secondSummary.MeasurementsInserted)
	require.Equal(t, 1, secondSummary.MeasurementsUpdated)

	var count int
	var storedValue float64
	require.NoError(t, database.QueryRow(`SELECT COUNT(*), MAX(value) FROM measurements`).Scan(&count, &storedValue))
	require.Equal(t, 1, count)
	require.Equal(t, 2.50, storedValue)
}

func TestImportReaderRejectsUnreadableXLSX(t *testing.T) {
	database := testsupport.DB(t)
	_, err := (Importer{DB: database}).ImportReader(context.Background(), "broken.xlsx", bytes.NewBufferString("not xlsx"), nil)
	require.ErrorContains(t, err, "open xlsx")
}

func parsedFixture(sha string, value float64, at time.Time) ParsedFile {
	return ParsedFile{
		SHA256:   sha,
		Filename: sha + ".xlsx",
		Meters: map[string]db.MeteringPoint{
			"AT001": {ID: "AT001", Direction: "CONSUMPTION"},
		},
		Measurements: []db.Measurement{{
			MeteringPointID: "AT001",
			Direction:       "CONSUMPTION",
			MetricKey:       "wirkenergie_kwh",
			MetricLabel:     "Wirkenergie [KWH]",
			IntervalStart:   at,
			Value:           value,
			Quality:         "L1",
		}},
	}
}
