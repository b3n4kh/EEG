package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct {
	*sql.DB
}

func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	conn.SetMaxOpenConns(1)
	db := &DB{DB: conn}
	if err := db.init(context.Background()); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) init(ctx context.Context) error {
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;`); err != nil {
		return fmt.Errorf("configure sqlite: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if err := db.ensureColumn(ctx, "users", "password_change_required", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := db.ensureColumn(ctx, "users", "eda_participant_key", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_users_eda_participant_key ON users (eda_participant_key) WHERE eda_participant_key <> ''`); err != nil {
		return fmt.Errorf("create users EDA participant index: %w", err)
	}
	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('admin', 'participant')),
  active INTEGER NOT NULL DEFAULT 1,
  password_change_required INTEGER NOT NULL DEFAULT 0,
  eda_participant_key TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS metering_points (
  id TEXT PRIMARY KEY,
  display_name TEXT NOT NULL DEFAULT '',
  direction TEXT NOT NULL DEFAULT '',
  network_operator TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS user_metering_points (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  metering_point_id TEXT NOT NULL REFERENCES metering_points(id) ON DELETE CASCADE,
  PRIMARY KEY (user_id, metering_point_id)
);

CREATE TABLE IF NOT EXISTS api_tokens (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  token_hash TEXT NOT NULL,
  active INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS import_batches (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  filename TEXT NOT NULL,
  sha256 TEXT NOT NULL UNIQUE,
  report_start TEXT,
  report_end TEXT,
  data_start TEXT,
  data_end TEXT,
  uploaded_by_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS measurements (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  metering_point_id TEXT NOT NULL REFERENCES metering_points(id) ON DELETE CASCADE,
  direction TEXT NOT NULL DEFAULT '',
  metric_key TEXT NOT NULL,
  metric_label TEXT NOT NULL,
  interval_start TEXT NOT NULL,
  value REAL NOT NULL,
  quality TEXT NOT NULL DEFAULT '',
  import_batch_id INTEGER NOT NULL REFERENCES import_batches(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (metering_point_id, direction, metric_key, interval_start)
);

CREATE TABLE IF NOT EXISTS overview_summaries (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  metering_point_id TEXT NOT NULL REFERENCES metering_points(id) ON DELETE CASCADE,
  direction TEXT NOT NULL DEFAULT '',
  network_operator TEXT NOT NULL DEFAULT '',
  report_start TEXT NOT NULL,
  report_end TEXT NOT NULL,
  metric_key TEXT NOT NULL,
  metric_label TEXT NOT NULL,
  value REAL NOT NULL,
  status TEXT NOT NULL DEFAULT '',
  quality TEXT NOT NULL DEFAULT '',
  import_batch_id INTEGER NOT NULL REFERENCES import_batches(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (metering_point_id, direction, report_start, report_end, metric_key)
);

CREATE INDEX IF NOT EXISTS idx_measurements_meter_time ON measurements (metering_point_id, interval_start);
CREATE INDEX IF NOT EXISTS idx_measurements_metric ON measurements (metric_key);
CREATE INDEX IF NOT EXISTS idx_summaries_meter ON overview_summaries (metering_point_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_eda_participant_key ON users (eda_participant_key) WHERE eda_participant_key <> '';
`

func (db *DB) ensureColumn(ctx context.Context, table, column, definition string) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return fmt.Errorf("inspect %s columns: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+column+` `+definition); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
}

func (db *DB) UpsertUser(ctx context.Context, username, displayName, passwordHash, role string, active bool) (int64, error) {
	if displayName == "" {
		displayName = username
	}
	res, err := db.ExecContext(ctx, `
INSERT INTO users (username, display_name, password_hash, role, active)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(username) DO UPDATE SET
  display_name = excluded.display_name,
  password_hash = excluded.password_hash,
  role = excluded.role,
  active = excluded.active,
  updated_at = CURRENT_TIMESTAMP
`, username, displayName, passwordHash, role, boolInt(active))
	if err != nil {
		return 0, err
	}
	if id, _ := res.LastInsertId(); id != 0 {
		return id, nil
	}
	user, err := db.UserByUsername(ctx, username)
	if err != nil {
		return 0, err
	}
	return user.ID, nil
}

func (db *DB) CreateUser(ctx context.Context, username, displayName, passwordHash, role string, active bool) error {
	if displayName == "" {
		displayName = username
	}
	_, err := db.ExecContext(ctx, `INSERT INTO users (username, display_name, password_hash, role, active) VALUES (?, ?, ?, ?, ?)`,
		username, displayName, passwordHash, role, boolInt(active))
	return err
}

func (db *DB) UpdateUser(ctx context.Context, id int64, displayName, role string, active bool) error {
	_, err := db.ExecContext(ctx, `UPDATE users SET display_name = ?, role = ?, active = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		displayName, role, boolInt(active), id)
	return err
}

func (db *DB) UpdatePassword(ctx context.Context, id int64, passwordHash string, requireChange bool) error {
	_, err := db.ExecContext(ctx, `UPDATE users SET password_hash = ?, password_change_required = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, passwordHash, boolInt(requireChange), id)
	return err
}

func (db *DB) UpsertEDAUser(ctx context.Context, participantKey, usernameBase, displayName, passwordHash string) (User, bool, error) {
	participantKey = strings.TrimSpace(participantKey)
	usernameBase = strings.Trim(strings.TrimSpace(usernameBase), "-.")
	displayName = strings.TrimSpace(displayName)
	if participantKey == "" {
		return User{}, false, errors.New("EDA participant key is required")
	}
	if usernameBase == "" {
		usernameBase = "teilnehmer"
	}
	if displayName == "" {
		displayName = usernameBase
	}
	existing, err := db.userByEDAParticipantKey(ctx, participantKey)
	if err == nil {
		if _, err := db.ExecContext(ctx, `
UPDATE users SET display_name = ?, role = ?, active = 1, updated_at = CURRENT_TIMESTAMP
WHERE id = ?`, displayName, RoleParticipant, existing.ID); err != nil {
			return User{}, false, err
		}
		user, err := db.UserByID(ctx, existing.ID)
		return user, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return User{}, false, err
	}
	for i := 1; i < 1000; i++ {
		username := usernameBase
		if i > 1 {
			username = fmt.Sprintf("%s-%d", usernameBase, i)
		}
		if _, err := db.UserByUsername(ctx, username); err == nil {
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return User{}, false, err
		}
		_, err := db.ExecContext(ctx, `
INSERT INTO users (username, display_name, password_hash, role, active, password_change_required, eda_participant_key)
VALUES (?, ?, ?, ?, 1, 1, ?)`, username, displayName, passwordHash, RoleParticipant, participantKey)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				continue
			}
			return User{}, false, err
		}
		user, err := db.UserByUsername(ctx, username)
		return user, true, err
	}
	return User{}, false, fmt.Errorf("could not allocate username for EDA participant %s", participantKey)
}

func (db *DB) UserByUsername(ctx context.Context, username string) (User, error) {
	return scanUser(db.QueryRowContext(ctx, userSelectSQL+` WHERE username = ?`, username))
}

func (db *DB) UserByID(ctx context.Context, id int64) (User, error) {
	return scanUser(db.QueryRowContext(ctx, userSelectSQL+` WHERE id = ?`, id))
}

func (db *DB) Users(ctx context.Context) ([]User, error) {
	rows, err := db.QueryContext(ctx, userSelectSQL+` ORDER BY role, username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

const userSelectSQL = `SELECT id, username, display_name, password_hash, role, active, eda_participant_key, password_change_required FROM users`

func (db *DB) userByEDAParticipantKey(ctx context.Context, participantKey string) (User, error) {
	return scanUser(db.QueryRowContext(ctx, userSelectSQL+` WHERE eda_participant_key = ?`, participantKey))
}

type userScanner interface {
	Scan(dest ...any) error
}

func scanUser(s userScanner) (User, error) {
	var u User
	var active, passwordChangeRequired int
	if err := s.Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.Role, &active, &u.EDAParticipantKey, &passwordChangeRequired); err != nil {
		return User{}, err
	}
	u.Active = active == 1
	u.PasswordChangeRequired = passwordChangeRequired == 1
	return u, nil
}

func (db *DB) UpsertAPIToken(ctx context.Context, name, hash string) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO api_tokens (name, token_hash, active)
VALUES (?, ?, 1)
ON CONFLICT(name) DO UPDATE SET token_hash = excluded.token_hash, active = 1
`, name, hash)
	return err
}

func (db *DB) ActiveTokenHashes(ctx context.Context) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT token_hash FROM api_tokens WHERE active = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, err
		}
		out = append(out, hash)
	}
	return out, rows.Err()
}

func (db *DB) UpsertMeteringPoint(ctx context.Context, tx *sql.Tx, mp MeteringPoint) error {
	exec := execer(db.DB)
	if tx != nil {
		exec = tx
	}
	_, err := exec.ExecContext(ctx, `
INSERT INTO metering_points (id, display_name, direction, network_operator)
VALUES (?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  direction = COALESCE(NULLIF(excluded.direction, ''), metering_points.direction),
  network_operator = COALESCE(NULLIF(excluded.network_operator, ''), metering_points.network_operator),
  updated_at = CURRENT_TIMESTAMP
`, mp.ID, mp.DisplayName, mp.Direction, mp.NetworkOperator)
	return err
}

func (db *DB) UpsertImportBatch(ctx context.Context, tx *sql.Tx, batch ImportBatch) (int64, error) {
	exec := execer(db.DB)
	queryer := queryRower(db.DB)
	if tx != nil {
		exec = tx
		queryer = tx
	}
	_, err := exec.ExecContext(ctx, `
INSERT INTO import_batches (filename, sha256, report_start, report_end, data_start, data_end, uploaded_by_user_id)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(sha256) DO UPDATE SET
  filename = excluded.filename,
  report_start = COALESCE(excluded.report_start, import_batches.report_start),
  report_end = COALESCE(excluded.report_end, import_batches.report_end),
  data_start = COALESCE(excluded.data_start, import_batches.data_start),
  data_end = COALESCE(excluded.data_end, import_batches.data_end)
`, batch.Filename, batch.SHA256, timePtrString(batch.ReportStart), timePtrString(batch.ReportEnd), timePtrString(batch.DataStart), timePtrString(batch.DataEnd), batch.UploadedByUserID)
	if err != nil {
		return 0, err
	}
	var id int64
	if err := queryer.QueryRowContext(ctx, `SELECT id FROM import_batches WHERE sha256 = ?`, batch.SHA256).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func (db *DB) UpsertMeasurement(ctx context.Context, tx *sql.Tx, m Measurement, batchID int64) (string, error) {
	exec := execer(db.DB)
	queryer := queryRower(db.DB)
	if tx != nil {
		exec = tx
		queryer = tx
	}
	var existingValue float64
	var existingQuality, existingLabel string
	err := queryer.QueryRowContext(ctx, `
SELECT value, quality, metric_label
FROM measurements
WHERE metering_point_id = ? AND direction = ? AND metric_key = ? AND interval_start = ?`,
		m.MeteringPointID, m.Direction, m.MetricKey, m.IntervalStart.UTC().Format(time.RFC3339)).
		Scan(&existingValue, &existingQuality, &existingLabel)
	if err == nil && existingValue == m.Value && existingQuality == m.Quality && existingLabel == m.MetricLabel {
		return "skipped", nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	status := "updated"
	if errors.Is(err, sql.ErrNoRows) {
		status = "inserted"
	}
	_, err = exec.ExecContext(ctx, `
INSERT INTO measurements (metering_point_id, direction, metric_key, metric_label, interval_start, value, quality, import_batch_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(metering_point_id, direction, metric_key, interval_start) DO UPDATE SET
  metric_label = excluded.metric_label,
  value = excluded.value,
  quality = excluded.quality,
  import_batch_id = excluded.import_batch_id,
  updated_at = CURRENT_TIMESTAMP
`, m.MeteringPointID, m.Direction, m.MetricKey, m.MetricLabel, m.IntervalStart.UTC().Format(time.RFC3339), m.Value, m.Quality, batchID)
	if err != nil {
		return "", err
	}
	return status, nil
}

func (db *DB) UpsertOverviewSummary(ctx context.Context, tx *sql.Tx, s OverviewSummary, batchID int64) (string, error) {
	exec := execer(db.DB)
	queryer := queryRower(db.DB)
	if tx != nil {
		exec = tx
		queryer = tx
	}
	var existingValue float64
	var existingStatus, existingQuality, existingLabel string
	err := queryer.QueryRowContext(ctx, `
SELECT value, status, quality, metric_label
FROM overview_summaries
WHERE metering_point_id = ? AND direction = ? AND report_start = ? AND report_end = ? AND metric_key = ?`,
		s.MeteringPointID, s.Direction, s.ReportStart.UTC().Format(time.RFC3339), s.ReportEnd.UTC().Format(time.RFC3339), s.MetricKey).
		Scan(&existingValue, &existingStatus, &existingQuality, &existingLabel)
	if err == nil && existingValue == s.Value && existingStatus == s.Status && existingQuality == s.Quality && existingLabel == s.MetricLabel {
		return "skipped", nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	status := "updated"
	if errors.Is(err, sql.ErrNoRows) {
		status = "inserted"
	}
	_, err = exec.ExecContext(ctx, `
INSERT INTO overview_summaries (metering_point_id, direction, network_operator, report_start, report_end, metric_key, metric_label, value, status, quality, import_batch_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(metering_point_id, direction, report_start, report_end, metric_key) DO UPDATE SET
  network_operator = excluded.network_operator,
  metric_label = excluded.metric_label,
  value = excluded.value,
  status = excluded.status,
  quality = excluded.quality,
  import_batch_id = excluded.import_batch_id,
  updated_at = CURRENT_TIMESTAMP
`, s.MeteringPointID, s.Direction, s.NetworkOperator, s.ReportStart.UTC().Format(time.RFC3339), s.ReportEnd.UTC().Format(time.RFC3339), s.MetricKey, s.MetricLabel, s.Value, s.Status, s.Quality, batchID)
	if err != nil {
		return "", err
	}
	return status, nil
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (db *DB) AssignMeters(ctx context.Context, userID int64, ids []string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_metering_points WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO user_metering_points (user_id, metering_point_id) VALUES (?, ?)`, userID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) AssignedMeterIDs(ctx context.Context, userID int64) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT metering_point_id FROM user_metering_points WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (db *DB) MeteringPoints(ctx context.Context, user *User) ([]MeterOverview, error) {
	var rows *sql.Rows
	var err error
	if user != nil && !user.IsAdmin() {
		rows, err = db.QueryContext(ctx, `
SELECT mp.id, mp.display_name, mp.direction, mp.network_operator
FROM metering_points mp
JOIN user_metering_points ump ON ump.metering_point_id = mp.id
WHERE ump.user_id = ? AND mp.id <> 'TOTAL'
ORDER BY mp.id`, user.ID)
	} else {
		rows, err = db.QueryContext(ctx, `SELECT id, display_name, direction, network_operator FROM metering_points ORDER BY CASE WHEN id = 'TOTAL' THEN 1 ELSE 0 END, id`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MeterOverview
	for rows.Next() {
		var m MeterOverview
		if err := rows.Scan(&m.ID, &m.DisplayName, &m.Direction, &m.NetworkOperator); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		totals, from, to, err := db.MeterTotals(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].MetricTotals = totals
		out[i].From = from
		out[i].To = to
	}
	return out, nil
}

func (db *DB) Meter(ctx context.Context, id string, user *User) (MeterOverview, error) {
	if user != nil && !user.IsAdmin() {
		ok, err := db.UserCanAccessMeter(ctx, user.ID, id)
		if err != nil {
			return MeterOverview{}, err
		}
		if !ok {
			return MeterOverview{}, sql.ErrNoRows
		}
	}
	row := db.QueryRowContext(ctx, `SELECT id, display_name, direction, network_operator FROM metering_points WHERE id = ?`, id)
	var m MeterOverview
	if err := row.Scan(&m.ID, &m.DisplayName, &m.Direction, &m.NetworkOperator); err != nil {
		return MeterOverview{}, err
	}
	totals, from, to, err := db.MeterTotals(ctx, id)
	if err != nil {
		return MeterOverview{}, err
	}
	m.MetricTotals, m.From, m.To = totals, from, to
	return m, nil
}

func (db *DB) UserCanAccessMeter(ctx context.Context, userID int64, meterID string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_metering_points WHERE user_id = ? AND metering_point_id = ?`, userID, meterID).Scan(&n)
	return n > 0, err
}

func (db *DB) MeterTotals(ctx context.Context, id string) ([]MetricTotal, *time.Time, *time.Time, error) {
	rows, err := db.QueryContext(ctx, `
SELECT metric_key, metric_label, SUM(value)
FROM measurements
WHERE metering_point_id = ?
GROUP BY metric_key, metric_label
ORDER BY metric_label`, id)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	var totals []MetricTotal
	for rows.Next() {
		var t MetricTotal
		if err := rows.Scan(&t.Key, &t.Label, &t.Sum); err != nil {
			return nil, nil, nil, err
		}
		totals = append(totals, t)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	var minS, maxS sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT MIN(interval_start), MAX(interval_start) FROM measurements WHERE metering_point_id = ?`, id).Scan(&minS, &maxS); err != nil {
		return nil, nil, nil, err
	}
	var from, to *time.Time
	if minS.Valid {
		if t, err := time.Parse(time.RFC3339, minS.String); err == nil {
			from = &t
		}
	}
	if maxS.Valid {
		if t, err := time.Parse(time.RFC3339, maxS.String); err == nil {
			to = &t
		}
	}
	return totals, from, to, nil
}

func (db *DB) MetricLabels(ctx context.Context, meterID string) ([]MetricTotal, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT metric_key, metric_label, 0 FROM measurements WHERE metering_point_id = ? ORDER BY metric_label`, meterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MetricTotal
	for rows.Next() {
		var m MetricTotal
		if err := rows.Scan(&m.Key, &m.Label, &m.Sum); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (db *DB) Series(ctx context.Context, meterID, metricKey string, limit int) ([]SeriesPoint, error) {
	if limit <= 0 {
		limit = 384
	}
	rows, err := db.QueryContext(ctx, `
SELECT interval_start, value
FROM measurements
WHERE metering_point_id = ? AND metric_key = ?
ORDER BY interval_start DESC
LIMIT ?`, meterID, metricKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rev []SeriesPoint
	for rows.Next() {
		var s string
		var p SeriesPoint
		if err := rows.Scan(&s, &p.Value); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil, err
		}
		p.IntervalStart = t
		rev = append(rev, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, nil
}

func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func rollback(tx *sql.Tx) {
	_ = tx.Rollback()
}

func timePtrString(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}
