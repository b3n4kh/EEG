package imports

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/ben/eeg-sumsum/internal/db"
)

const defaultSampleWorkbook = "testdata/sample.xlsx"

func TestParseSampleWorkbook(t *testing.T) {
	path, content := readSample(t)
	parsed, err := Parse(filepath.Base(path), content)
	if err != nil {
		t.Fatalf("parse sample workbook: %v", err)
	}
	if len(parsed.Meters) != 7 {
		t.Fatalf("meters = %d, want 7", len(parsed.Meters))
	}
	if len(parsed.Measurements) == 0 {
		t.Fatal("expected measurements")
	}
	if len(parsed.Summaries) == 0 {
		t.Fatal("expected overview summaries")
	}
	if parsed.ReportStart == nil || parsed.ReportEnd == nil || parsed.DataStart == nil || parsed.DataEnd == nil {
		t.Fatal("expected parsed report and data periods")
	}
}

func TestImportSameWorkbookDoesNotDuplicateMeasurements(t *testing.T) {
	path, content := readSample(t)
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	importer := Importer{DB: database}
	first, err := importer.ImportReader(context.Background(), filepath.Base(path), bytes.NewReader(content), nil)
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	second, err := importer.ImportReader(context.Background(), filepath.Base(path), bytes.NewReader(content), nil)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if first.MeasurementsInserted == 0 {
		t.Fatal("expected first import to insert measurements")
	}
	if second.MeasurementsInserted != 0 {
		t.Fatalf("second import inserted %d measurements, want 0", second.MeasurementsInserted)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM measurements`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != first.MeasurementsRead {
		t.Fatalf("stored measurements = %d, want %d", count, first.MeasurementsRead)
	}
}

func TestParseMissingSheetsReturnsValidationError(t *testing.T) {
	f := excelize.NewFile()
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse("empty.xlsx", buf.Bytes()); err == nil {
		t.Fatal("expected error for missing required sheets")
	}
}

func readSample(t *testing.T) (string, []byte) {
	t.Helper()
	path := os.Getenv("EEG_SAMPLE_XLSX")
	if path == "" {
		path = defaultSampleWorkbook
	}
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Skipf("sample workbook not found: %s", path)
	}
	if err != nil {
		t.Fatal(err)
	}
	return path, content
}
