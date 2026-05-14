package imports

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/ben/eeg-sumsum/internal/db"
)

var nonKey = regexp.MustCompile(`[^a-z0-9]+`)

type Importer struct {
	DB *db.DB
}

type ParsedFile struct {
	SHA256       string
	Filename     string
	ReportStart  *time.Time
	ReportEnd    *time.Time
	DataStart    *time.Time
	DataEnd      *time.Time
	Meters       map[string]db.MeteringPoint
	Measurements []db.Measurement
	Summaries    []db.OverviewSummary
}

func (i Importer) ImportReader(ctx context.Context, filename string, r io.Reader, uploadedBy *int64) (db.ImportSummary, error) {
	content, err := io.ReadAll(r)
	if err != nil {
		return db.ImportSummary{}, fmt.Errorf("read upload: %w", err)
	}
	parsed, err := Parse(filename, content)
	if err != nil {
		return db.ImportSummary{}, err
	}
	tx, err := i.DB.BeginTx(ctx, nil)
	if err != nil {
		return db.ImportSummary{}, err
	}
	defer tx.Rollback()

	batchID, err := i.DB.UpsertImportBatch(ctx, tx, db.ImportBatch{
		Filename:         parsed.Filename,
		SHA256:           parsed.SHA256,
		ReportStart:      parsed.ReportStart,
		ReportEnd:        parsed.ReportEnd,
		DataStart:        parsed.DataStart,
		DataEnd:          parsed.DataEnd,
		UploadedByUserID: uploadedBy,
	})
	if err != nil {
		return db.ImportSummary{}, fmt.Errorf("store import batch: %w", err)
	}
	summary := db.ImportSummary{
		BatchID:          batchID,
		Filename:         parsed.Filename,
		SHA256:           parsed.SHA256,
		MeasurementsRead: len(parsed.Measurements),
		SummariesRead:    len(parsed.Summaries),
	}
	for _, mp := range parsed.Meters {
		if err := i.DB.UpsertMeteringPoint(ctx, tx, mp); err != nil {
			return db.ImportSummary{}, fmt.Errorf("store metering point %s: %w", mp.ID, err)
		}
	}
	for _, m := range parsed.Measurements {
		status, err := i.DB.UpsertMeasurement(ctx, tx, m, batchID)
		if err != nil {
			return db.ImportSummary{}, fmt.Errorf("store measurement %s %s %s: %w", m.MeteringPointID, m.MetricKey, m.IntervalStart, err)
		}
		switch status {
		case "inserted":
			summary.MeasurementsInserted++
		case "updated":
			summary.MeasurementsUpdated++
		default:
			summary.MeasurementsSkipped++
		}
	}
	for _, s := range parsed.Summaries {
		status, err := i.DB.UpsertOverviewSummary(ctx, tx, s, batchID)
		if err != nil {
			return db.ImportSummary{}, fmt.Errorf("store overview %s %s: %w", s.MeteringPointID, s.MetricKey, err)
		}
		switch status {
		case "inserted":
			summary.SummariesInserted++
		case "updated":
			summary.SummariesUpdated++
		}
	}
	if err := tx.Commit(); err != nil {
		return db.ImportSummary{}, err
	}
	return summary, nil
}

func Parse(filename string, content []byte) (ParsedFile, error) {
	hash := sha256.Sum256(content)
	f, err := excelize.OpenReader(bytes.NewReader(content))
	if err != nil {
		return ParsedFile{}, fmt.Errorf("open xlsx: %w", err)
	}
	defer f.Close()

	out := ParsedFile{
		SHA256:   hex.EncodeToString(hash[:]),
		Filename: filepath.Base(filename),
		Meters:   map[string]db.MeteringPoint{},
	}
	if err := parseEnergy(f, &out); err != nil {
		return ParsedFile{}, err
	}
	if err := parseOverview(f, &out); err != nil {
		return ParsedFile{}, err
	}
	return out, nil
}

func parseEnergy(f *excelize.File, out *ParsedFile) error {
	rows, err := f.GetRows("Energiedaten")
	if err != nil {
		return fmt.Errorf("read sheet Energiedaten: %w", err)
	}
	if len(rows) < 17 {
		return fmt.Errorf("sheet Energiedaten is missing metadata or data rows")
	}
	meta := rows[:16]
	for col := 1; col < len(meta[1]); col += 2 {
		meterID := cell(meta, 1, col)
		metricLabel := cell(meta, 13, col)
		if meterID == "" || metricLabel == "" {
			continue
		}
		direction := cell(meta, 3, col)
		reportStart := parseOptionalTime(cell(meta, 4, col))
		reportEnd := parseOptionalTime(cell(meta, 5, col))
		dataStart := parseOptionalTime(cell(meta, 6, col))
		dataEnd := parseOptionalTime(cell(meta, 7, col))
		setMinTime(&out.ReportStart, reportStart)
		setMaxTime(&out.ReportEnd, reportEnd)
		setMinTime(&out.DataStart, dataStart)
		setMaxTime(&out.DataEnd, dataEnd)

		out.Meters[meterID] = db.MeteringPoint{ID: meterID, Direction: direction}
		metricKey := MetricKey(metricLabel)
		for rowIdx := 16; rowIdx < len(rows); rowIdx++ {
			interval, ok := parseTime(cell(rows, rowIdx, 0))
			if !ok {
				continue
			}
			value, ok := parseFloat(cell(rows, rowIdx, col))
			if !ok {
				continue
			}
			out.Measurements = append(out.Measurements, db.Measurement{
				MeteringPointID: meterID,
				Direction:       direction,
				MetricKey:       metricKey,
				MetricLabel:     metricLabel,
				IntervalStart:   interval,
				Value:           value,
				Quality:         cell(rows, rowIdx, col+1),
			})
		}
	}
	return nil
}

func parseOverview(f *excelize.File, out *ParsedFile) error {
	rows, err := f.GetRows("Übersicht")
	if err != nil {
		return fmt.Errorf("read sheet Übersicht: %w", err)
	}
	if len(rows) < 7 {
		return fmt.Errorf("sheet Übersicht is missing summary rows")
	}
	headers := rows[5]
	for rowIdx := 6; rowIdx < len(rows); rowIdx++ {
		row := rows[rowIdx]
		meterID := val(row, 0)
		if meterID == "" || meterID == "Zählpunkt" {
			continue
		}
		direction := val(row, 1)
		operator := val(row, 2)
		reportStart, okStart := parseTime(val(row, 3))
		reportEnd, okEnd := parseTime(val(row, 4))
		if !okStart || !okEnd {
			continue
		}
		out.Meters[meterID] = db.MeteringPoint{
			ID:              meterID,
			Direction:       direction,
			NetworkOperator: operator,
		}
		setMinTime(&out.ReportStart, &reportStart)
		setMaxTime(&out.ReportEnd, &reportEnd)
		for col := 5; col <= 13 && col < len(headers); col++ {
			label := val(headers, col)
			if label == "" {
				continue
			}
			value, ok := parseFloat(val(row, col))
			if !ok {
				continue
			}
			out.Summaries = append(out.Summaries, db.OverviewSummary{
				MeteringPointID: meterID,
				Direction:       direction,
				NetworkOperator: operator,
				ReportStart:     reportStart,
				ReportEnd:       reportEnd,
				MetricKey:       MetricKey(label),
				MetricLabel:     label,
				Value:           value,
				Status:          val(row, 14),
				Quality:         val(row, 15),
			})
		}
	}
	return nil
}

func MetricKey(label string) string {
	key := strings.ToLower(label)
	replacer := strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss", "é", "e")
	key = replacer.Replace(key)
	key = nonKey.ReplaceAllString(key, "_")
	key = strings.Trim(key, "_")
	if key == "" {
		return "unknown"
	}
	return key
}

func parseOptionalTime(s string) *time.Time {
	if t, ok := parseTime(s); ok {
		return &t
	}
	return nil
}

func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	loc, _ := time.LoadLocation("Europe/Vienna")
	for _, layout := range []string{"02.01.2006 15:04:05", "02.01.2006 15:04"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func parseFloat(s string) (float64, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

func cell(rows [][]string, row, col int) string {
	if row < 0 || row >= len(rows) {
		return ""
	}
	return val(rows[row], col)
}

func val(row []string, col int) string {
	if col < 0 || col >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[col])
}

func setMinTime(dst **time.Time, candidate *time.Time) {
	if candidate == nil {
		return
	}
	if *dst == nil || candidate.Before(**dst) {
		t := *candidate
		*dst = &t
	}
}

func setMaxTime(dst **time.Time, candidate *time.Time) {
	if candidate == nil {
		return
	}
	if *dst == nil || candidate.After(**dst) {
		t := *candidate
		*dst = &t
	}
}
