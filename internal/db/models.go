package db

import "time"

const (
	RoleAdmin       = "admin"
	RoleParticipant = "participant"
)

type User struct {
	ID                     int64
	Username               string
	DisplayName            string
	Role                   string
	Active                 bool
	PasswordHash           string
	EDAParticipantKey      string
	PasswordChangeRequired bool
}

func (u User) IsAdmin() bool {
	return u.Role == RoleAdmin
}

type MeteringPoint struct {
	ID              string
	DisplayName     string
	Direction       string
	NetworkOperator string
}

type MetricTotal struct {
	Key   string
	Label string
	Sum   float64
}

type MeterOverview struct {
	MeteringPoint
	MetricTotals []MetricTotal
	From         *time.Time
	To           *time.Time
}

type Measurement struct {
	MeteringPointID string
	Direction       string
	MetricKey       string
	MetricLabel     string
	IntervalStart   time.Time
	Value           float64
	Quality         string
}

type SeriesPoint struct {
	IntervalStart time.Time
	Value         float64
}

type ImportSummary struct {
	BatchID              int64  `json:"batch_id"`
	Filename             string `json:"filename"`
	SHA256               string `json:"sha256"`
	MeasurementsRead     int    `json:"measurements_read"`
	MeasurementsInserted int    `json:"measurements_inserted"`
	MeasurementsUpdated  int    `json:"measurements_updated"`
	MeasurementsSkipped  int    `json:"measurements_skipped"`
	SummariesRead        int    `json:"summaries_read"`
	SummariesInserted    int    `json:"summaries_inserted"`
	SummariesUpdated     int    `json:"summaries_updated"`
}

type ImportBatch struct {
	ID               int64
	Filename         string
	SHA256           string
	ReportStart      *time.Time
	ReportEnd        *time.Time
	DataStart        *time.Time
	DataEnd          *time.Time
	UploadedByUserID *int64
}

type OverviewSummary struct {
	MeteringPointID string
	Direction       string
	NetworkOperator string
	ReportStart     time.Time
	ReportEnd       time.Time
	MetricKey       string
	MetricLabel     string
	Value           float64
	Status          string
	Quality         string
}
