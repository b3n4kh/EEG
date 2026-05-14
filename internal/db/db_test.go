package db_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ben/eeg-sumsum/internal/db"
	"github.com/ben/eeg-sumsum/internal/testsupport"
)

func TestUpsertMeasurementReportsInsertedSkippedAndUpdated(t *testing.T) {
	ctx := context.Background()
	database := testsupport.DB(t)
	testsupport.UpsertMeter(t, database, db.MeteringPoint{ID: "AT001", Direction: "CONSUMPTION"})
	batchID := testsupport.SeedBatch(t, database, "measurement-status")
	vienna, err := time.LoadLocation("Europe/Vienna")
	require.NoError(t, err)
	interval := time.Date(2026, 5, 1, 1, 30, 0, 0, vienna)
	measurement := db.Measurement{
		MeteringPointID: "AT001",
		Direction:       "CONSUMPTION",
		MetricKey:       "wirkenergie_kwh",
		MetricLabel:     "Wirkenergie [KWH]",
		IntervalStart:   interval,
		Value:           1.25,
		Quality:         "L1",
	}

	status, err := database.UpsertMeasurement(ctx, nil, measurement, batchID)
	require.NoError(t, err)
	require.Equal(t, "inserted", status)
	status, err = database.UpsertMeasurement(ctx, nil, measurement, batchID)
	require.NoError(t, err)
	require.Equal(t, "skipped", status)
	measurement.Value = 2.5
	status, err = database.UpsertMeasurement(ctx, nil, measurement, batchID)
	require.NoError(t, err)
	require.Equal(t, "updated", status)

	var storedTime string
	var storedValue float64
	require.NoError(t, database.QueryRowContext(ctx, `SELECT interval_start, value FROM measurements WHERE metering_point_id = ?`, "AT001").Scan(&storedTime, &storedValue))
	require.Equal(t, "2026-04-30T23:30:00Z", storedTime)
	require.Equal(t, 2.5, storedValue)
}

func TestAssignMetersReplacesExistingAssignments(t *testing.T) {
	ctx := context.Background()
	database := testsupport.DB(t)
	user := testsupport.CreateUser(t, database, "participant", "Participant", "secret12345", db.RoleParticipant, true)
	for _, id := range []string{"AT001", "AT002", "AT003"} {
		testsupport.UpsertMeter(t, database, db.MeteringPoint{ID: id, Direction: "CONSUMPTION"})
	}
	testsupport.AssignMeters(t, database, user, "AT001", "AT002")
	testsupport.AssignMeters(t, database, user, "AT002", "AT003", " ")

	assigned, err := database.AssignedMeterIDs(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"AT002": true, "AT003": true}, assigned)
}

func TestScheduledImportStatusRecordsStartedAndFinished(t *testing.T) {
	ctx := context.Background()
	database := testsupport.DB(t)
	started := time.Date(2026, 5, 14, 1, 0, 0, 0, time.UTC)
	finished := started.Add(2 * time.Minute)

	status, err := database.ScheduledImportStatus(ctx, "eda_auto_import")
	require.NoError(t, err)
	require.Equal(t, "eda_auto_import", status.Name)
	require.Nil(t, status.LastStartedAt)

	require.NoError(t, database.RecordScheduledImportStarted(ctx, "eda_auto_import", started))
	status, err = database.ScheduledImportStatus(ctx, "eda_auto_import")
	require.NoError(t, err)
	require.Equal(t, started, *status.LastStartedAt)
	require.Nil(t, status.LastFinishedAt)
	require.Nil(t, status.LastSuccess)
	require.Equal(t, "running", status.LastResult)

	require.NoError(t, database.RecordScheduledImportFinished(ctx, "eda_auto_import", started, finished, true, "13 gelesen", ""))
	status, err = database.ScheduledImportStatus(ctx, "eda_auto_import")
	require.NoError(t, err)
	require.Equal(t, finished, *status.LastFinishedAt)
	require.NotNil(t, status.LastSuccess)
	require.True(t, *status.LastSuccess)
	require.Equal(t, "13 gelesen", status.LastResult)
}

func TestParticipantVisibilityExcludesTotalAndUnassignedMeters(t *testing.T) {
	ctx := context.Background()
	database := testsupport.DB(t)
	user := testsupport.CreateUser(t, database, "participant", "Participant", "secret12345", db.RoleParticipant, true)
	for _, id := range []string{"AT001", "AT002", "TOTAL"} {
		testsupport.UpsertMeter(t, database, db.MeteringPoint{ID: id, Direction: "CONSUMPTION"})
	}
	testsupport.AssignMeters(t, database, user, "AT001", "TOTAL")

	meters, err := database.MeteringPoints(ctx, &user)
	require.NoError(t, err)
	require.Len(t, meters, 1)
	require.Equal(t, "AT001", meters[0].ID)

	_, err = database.Meter(ctx, "AT002", &user)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = database.Meter(ctx, "TOTAL", &user)
	require.ErrorIs(t, err, sql.ErrNoRows)

	admin := db.User{ID: 99, Role: db.RoleAdmin, Active: true}
	total, err := database.Meter(ctx, "TOTAL", &admin)
	require.NoError(t, err)
	require.Equal(t, "TOTAL", total.ID)
}

func TestParticipantMeterSummariesAggregatePerAssignedMeter(t *testing.T) {
	ctx := context.Background()
	database := testsupport.DB(t)
	user := testsupport.CreateUser(t, database, "participant", "Participant", "secret12345", db.RoleParticipant, true)
	for _, id := range []string{"AT001", "AT002", "AT003", "TOTAL"} {
		testsupport.UpsertMeter(t, database, db.MeteringPoint{ID: id, Direction: "CONSUMPTION"})
	}
	testsupport.AssignMeters(t, database, user, "AT001", "AT002")
	batchID := testsupport.SeedBatch(t, database, "summary")
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	testsupport.AddMeasurement(t, database, batchID, "AT001", "CONSUMPTION", db.MetricCommunityShareKey, db.MetricCommunityShareLabel, start, 50)
	testsupport.AddMeasurement(t, database, batchID, "AT001", "CONSUMPTION", db.MetricTotalConsumptionKey, db.MetricTotalConsumptionLabel, start.Add(15*time.Minute), 200)
	testsupport.AddMeasurement(t, database, batchID, "AT002", "CONSUMPTION", db.MetricCommunityShareKey, db.MetricCommunityShareLabel, start, 300)
	testsupport.AddMeasurement(t, database, batchID, "AT002", "CONSUMPTION", db.MetricTotalConsumptionKey, db.MetricTotalConsumptionLabel, start, 600)
	testsupport.AddMeasurement(t, database, batchID, "AT003", "CONSUMPTION", db.MetricCommunityShareKey, db.MetricCommunityShareLabel, start, 900)
	testsupport.AddMeasurement(t, database, batchID, "TOTAL", "CONSUMPTION", db.MetricCommunityShareKey, db.MetricCommunityShareLabel, start, 999)

	summaries, err := database.ParticipantMeterSummaries(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, summaries, 2)
	require.Equal(t, "AT001", summaries[0].ID)
	require.Equal(t, 50.0, summaries[0].CommunityShareKWh)
	require.Equal(t, 200.0, summaries[0].TotalConsumptionKWh)
	require.Equal(t, 25.0, summaries[0].CoveragePercent)
	require.Equal(t, start, *summaries[0].From)
	require.Equal(t, start.Add(15*time.Minute), *summaries[0].To)
	require.Equal(t, "AT002", summaries[1].ID)
	require.Equal(t, 50.0, summaries[1].CoveragePercent)
}
