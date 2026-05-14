package web

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/ben/eeg-sumsum/internal/db"
	"github.com/ben/eeg-sumsum/internal/views"
)

const (
	autoEDAImportJobName      = "eda_auto_import"
	defaultEDAAutoImportCron  = "15 3 * * *"
	defaultEDAAutoImportDays  = 30
	defaultEDAAutoImportLimit = 30 * time.Minute
)

type EDAAutoImportConfig struct {
	Enabled      bool
	Schedule     string
	LookbackDays int
	Timeout      time.Duration
}

type EDAAutoImporter struct {
	cron         *cron.Cron
	entryID      cron.EntryID
	schedule     cron.Schedule
	scheduleSpec string
	location     *time.Location
	lookbackDays int
	cancel       context.CancelFunc
}

func (s *Server) StartEDAAutoImport(ctx context.Context, cfg EDAAutoImportConfig) error {
	if !cfg.Enabled || !s.EDA.Config.Enabled() {
		s.edaAuto = nil
		return nil
	}
	location := viennaLocation()
	if cfg.Schedule == "" {
		cfg.Schedule = defaultEDAAutoImportCron
	}
	if cfg.LookbackDays <= 0 {
		cfg.LookbackDays = defaultEDAAutoImportDays
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultEDAAutoImportLimit
	}
	schedule, err := cron.ParseStandard(cfg.Schedule)
	if err != nil {
		return fmt.Errorf("parse EDA_AUTO_IMPORT_CRON: %w", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	runner := cron.New(
		cron.WithLocation(location),
		cron.WithLogger(cronSlogLogger{}),
		cron.WithChain(
			cron.SkipIfStillRunning(cronSlogLogger{}),
			cron.Recover(cronSlogLogger{}),
		),
	)
	auto := &EDAAutoImporter{
		cron:         runner,
		schedule:     schedule,
		scheduleSpec: cfg.Schedule,
		location:     location,
		lookbackDays: cfg.LookbackDays,
		cancel:       cancel,
	}
	auto.entryID = runner.Schedule(schedule, cron.FuncJob(func() {
		if runCtx.Err() != nil {
			return
		}
		jobCtx, jobCancel := context.WithTimeout(runCtx, cfg.Timeout)
		defer jobCancel()
		if err := s.runScheduledEDAImport(jobCtx, time.Now()); err != nil {
			slog.Error("scheduled EDA import failed", "error", err)
		}
	}))
	s.edaAuto = auto
	runner.Start()
	slog.Info("scheduled EDA auto import",
		"schedule", cfg.Schedule,
		"timezone", location.String(),
		"lookback_days", cfg.LookbackDays,
		"next_run", auto.NextRun(),
	)
	return nil
}

func (s *Server) StopEDAAutoImport(ctx context.Context) error {
	if s.edaAuto == nil {
		return nil
	}
	if s.edaAuto.cancel != nil {
		s.edaAuto.cancel()
	}
	stopped := s.edaAuto.cron.Stop()
	select {
	case <-stopped.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *EDAAutoImporter) NextRun() *time.Time {
	if a == nil {
		return nil
	}
	if entry := a.cron.Entry(a.entryID); entry.Valid() && !entry.Next.IsZero() {
		next := entry.Next
		return &next
	}
	if a.schedule != nil {
		next := a.schedule.Next(time.Now().In(a.location))
		if !next.IsZero() {
			return &next
		}
	}
	return nil
}

func (s *Server) runScheduledEDAImport(ctx context.Context, now time.Time) error {
	auto := s.edaAuto
	lookbackDays := defaultEDAAutoImportDays
	if auto != nil && auto.lookbackDays > 0 {
		lookbackDays = auto.lookbackDays
	}
	from, to := edaAutoImportRange(now, lookbackDays, viennaLocation())
	started := now.UTC()
	if err := s.DB.RecordScheduledImportStarted(ctx, autoEDAImportJobName, started); err != nil {
		return fmt.Errorf("record scheduled EDA import start: %w", err)
	}
	summary, err := s.importEDA(ctx, from, to, nil)
	finished := time.Now().UTC()
	recordCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err != nil {
		recordErr := s.DB.RecordScheduledImportFinished(recordCtx, autoEDAImportJobName, started, finished, false, "failed", err.Error())
		if recordErr != nil {
			return fmt.Errorf("%w; record scheduled EDA import failure: %v", err, recordErr)
		}
		return err
	}
	result := formatImportSummary(summary)
	if err := s.DB.RecordScheduledImportFinished(recordCtx, autoEDAImportJobName, started, finished, true, result, ""); err != nil {
		return fmt.Errorf("record scheduled EDA import result: %w", err)
	}
	return nil
}

func edaAutoImportRange(now time.Time, lookbackDays int, loc *time.Location) (time.Time, time.Time) {
	if lookbackDays <= 0 {
		lookbackDays = defaultEDAAutoImportDays
	}
	endDay := now.In(loc).AddDate(0, 0, -1)
	to := time.Date(endDay.Year(), endDay.Month(), endDay.Day(), 23, 45, 0, 0, loc)
	fromDay := endDay.AddDate(0, 0, -(lookbackDays - 1))
	from := time.Date(fromDay.Year(), fromDay.Month(), fromDay.Day(), 0, 0, 0, 0, loc)
	return from, to
}

func (s *Server) edaAutoImportStatus(ctx context.Context) (views.EDAAutoImportStatus, error) {
	stored, err := s.DB.ScheduledImportStatus(ctx, autoEDAImportJobName)
	if err != nil {
		return views.EDAAutoImportStatus{}, err
	}
	status := views.EDAAutoImportStatus{
		Enabled:        s.edaAuto != nil,
		Schedule:       defaultEDAAutoImportCron,
		LookbackDays:   defaultEDAAutoImportDays,
		LastStartedAt:  stored.LastStartedAt,
		LastFinishedAt: stored.LastFinishedAt,
		LastSuccess:    stored.LastSuccess,
		LastResult:     stored.LastResult,
		LastError:      stored.LastError,
	}
	if s.edaAuto != nil {
		status.Schedule = s.edaAuto.scheduleSpec
		status.LookbackDays = s.edaAuto.lookbackDays
		status.NextRun = s.edaAuto.NextRun()
	}
	return status, nil
}

func formatImportSummary(summary db.ImportSummary) string {
	return fmt.Sprintf("%d gelesen, %d neu, %d aktualisiert, %d unverändert",
		summary.MeasurementsRead,
		summary.MeasurementsInserted,
		summary.MeasurementsUpdated,
		summary.MeasurementsSkipped,
	)
}

func viennaLocation() *time.Location {
	loc, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		return time.Local
	}
	return loc
}

type cronSlogLogger struct{}

func (cronSlogLogger) Info(msg string, keysAndValues ...any) {
	slog.Info("cron "+msg, keysAndValues...)
}

func (cronSlogLogger) Error(err error, msg string, keysAndValues ...any) {
	slog.Error("cron "+msg, append(keysAndValues, "error", err)...)
}
