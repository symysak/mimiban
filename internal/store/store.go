// Package store wraps the SQLite database (pure Go driver).
package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Call is one row of the calls table.
type Call struct {
	ID         int64    `json:"id"`
	ReceiverID string   `json:"receiver_id"`
	Channel    string   `json:"channel"`
	StartedAt  string   `json:"started_at"`
	Duration   float64  `json:"duration"`
	PeakDB     *float64 `json:"peak_db"`
	Path       string   `json:"path"`
	Priority   int      `json:"priority"`
	Transcript *string  `json:"transcript"`
	ASRStatus  string   `json:"asr_status"`
	ASRModel   *string  `json:"asr_model"`
	ASRError   *string  `json:"asr_error"`
	ASRNote    *string  `json:"asr_note"`
}

// NotifyLog is one row of notify_log.
type NotifyLog struct {
	ID         int64   `json:"id"`
	CallID     *int64  `json:"call_id"`
	WebhookID  string  `json:"webhook_id"`
	SentAt     string  `json:"sent_at"`
	Status     string  `json:"status"`
	HTTPStatus *int    `json:"http_status"`
	Error      *string `json:"error"`
}

// DB is the store handle.
type DB struct {
	db *sql.DB
}

const schemaVersion = 2

// Open opens (and migrates) the database at path.
func Open(path string) (*DB, error) {
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sdb.SetMaxOpenConns(1)
	d := &DB{db: sdb}
	if err := d.migrate(); err != nil {
		sdb.Close()
		return nil, err
	}
	return d, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

func (d *DB) migrate() error {
	var v int
	if err := d.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v < 1 {
		_, err := d.db.Exec(`
CREATE TABLE IF NOT EXISTS calls (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  receiver_id  TEXT NOT NULL,
  channel      TEXT NOT NULL,
  started_at   TEXT NOT NULL,
  duration     REAL NOT NULL,
  peak_db      REAL,
  path         TEXT NOT NULL,
  priority     INTEGER NOT NULL DEFAULT 0,
  transcript   TEXT,
  asr_status   TEXT NOT NULL DEFAULT 'pending',
  asr_model    TEXT,
  asr_error    TEXT
);
CREATE INDEX IF NOT EXISTS idx_calls_rx_time ON calls(receiver_id, started_at);
CREATE INDEX IF NOT EXISTS idx_calls_status  ON calls(asr_status, priority DESC, id);
CREATE TABLE IF NOT EXISTS notify_log (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  call_id      INTEGER,
  webhook_id   TEXT NOT NULL,
  sent_at      TEXT NOT NULL,
  status       TEXT NOT NULL,
  http_status  INTEGER,
  error        TEXT
);
PRAGMA user_version = 1;`)
		if err != nil {
			return err
		}
	}
	if v < 2 {
		if _, err := d.db.Exec(`ALTER TABLE calls ADD COLUMN asr_note TEXT`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
		if _, err := d.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			return err
		}
	}
	return nil
}

// FormatTime renders a local time as ISO 8601 with offset.
func FormatTime(t time.Time) string { return t.Format("2006-01-02T15:04:05.000-07:00") }

// InsertCall adds a call and returns its id.
func (d *DB) InsertCall(c *Call) (int64, error) {
	res, err := d.db.Exec(`INSERT INTO calls(receiver_id, channel, started_at, duration, peak_db, path, priority, asr_status)
		VALUES(?,?,?,?,?,?,?,?)`, c.ReceiverID, c.Channel, c.StartedAt, c.Duration, c.PeakDB, c.Path, c.Priority, c.ASRStatus)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	c.ID = id
	return id, err
}

const callCols = `id, receiver_id, channel, started_at, duration, peak_db, path, priority, transcript, asr_status, asr_model, asr_error, asr_note`

func scanCall(sc interface{ Scan(...any) error }) (*Call, error) {
	var c Call
	if err := sc.Scan(&c.ID, &c.ReceiverID, &c.Channel, &c.StartedAt, &c.Duration, &c.PeakDB, &c.Path, &c.Priority,
		&c.Transcript, &c.ASRStatus, &c.ASRModel, &c.ASRError, &c.ASRNote); err != nil {
		return nil, err
	}
	return &c, nil
}

// GetCall fetches one call.
func (d *DB) GetCall(id int64) (*Call, error) {
	return scanCall(d.db.QueryRow(`SELECT `+callCols+` FROM calls WHERE id=?`, id))
}

// ListFilter are the query options of ListCalls.
type ListFilter struct {
	Date        string // YYYY-MM-DD (local)
	ReceiverIDs []string
	Query       string
	Limit       int
	Offset      int
}

// ListCalls returns matching calls newest first plus the total count.
func (d *DB) ListCalls(f ListFilter) ([]*Call, int, error) {
	where, args := buildWhere(f)
	var total int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM calls`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	rows, err := d.db.Query(`SELECT `+callCols+` FROM calls`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*Call
	for rows.Next() {
		c, err := scanCall(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	if out == nil {
		out = []*Call{}
	}
	return out, total, rows.Err()
}

func buildWhere(f ListFilter) (string, []any) {
	var conds []string
	var args []any
	if f.Date != "" {
		conds = append(conds, "substr(started_at,1,10) = ?")
		args = append(args, f.Date)
	}
	if len(f.ReceiverIDs) > 0 {
		ph := make([]string, len(f.ReceiverIDs))
		for i, id := range f.ReceiverIDs {
			ph[i] = "?"
			args = append(args, id)
		}
		conds = append(conds, "receiver_id IN ("+strings.Join(ph, ",")+")")
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		conds = append(conds, "transcript LIKE ? ESCAPE '\\'")
		esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
		args = append(args, "%"+esc+"%")
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// UpdateTranscript stores a manual/ASR result.
func (d *DB) UpdateTranscript(id int64, text, status string, model, asrErr, note *string) error {
	_, err := d.db.Exec(`UPDATE calls SET transcript=?, asr_status=?, asr_model=?, asr_error=?, asr_note=? WHERE id=?`,
		text, status, model, asrErr, note, id)
	return err
}

// SetStatus updates only the asr_status (and clears the error when not error).
func (d *DB) SetStatus(id int64, status string, asrErr *string) error {
	_, err := d.db.Exec(`UPDATE calls SET asr_status=?, asr_error=? WHERE id=?`, status, asrErr, id)
	return err
}

// ResetStatus marks the given set for re-recognition. which: "one"(id) / "pending" / "all".
func (d *DB) ResetStatus(which string, id int64) (int64, error) {
	var res sql.Result
	var err error
	switch which {
	case "one":
		res, err = d.db.Exec(`UPDATE calls SET asr_status='pending', asr_error=NULL WHERE id=?`, id)
	case "pending":
		res, err = d.db.Exec(`UPDATE calls SET asr_status='pending', asr_error=NULL WHERE asr_status IN ('error','skipped','running')`)
	default:
		res, err = d.db.Exec(`UPDATE calls SET asr_status='pending', asr_error=NULL`)
	}
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ResetRunning turns rows left in 'running' by a previous crash back to pending.
func (d *DB) ResetRunning() (int64, error) {
	res, err := d.db.Exec(`UPDATE calls SET asr_status='pending' WHERE asr_status='running'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// NextPending returns the next call to transcribe or nil.
func (d *DB) NextPending() (*Call, error) {
	c, err := scanCall(d.db.QueryRow(`SELECT ` + callCols + ` FROM calls WHERE asr_status='pending' ORDER BY priority DESC, id ASC LIMIT 1`))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

// PendingCounts returns the number of pending calls per receiver.
func (d *DB) PendingCounts() (map[string]int, int, error) {
	rows, err := d.db.Query(`SELECT receiver_id, COUNT(*) FROM calls WHERE asr_status IN ('pending','running') GROUP BY receiver_id`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	m := map[string]int{}
	total := 0
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, 0, err
		}
		m[id] = n
		total += n
	}
	return m, total, rows.Err()
}

// DeleteCall removes a row and returns its path.
func (d *DB) DeleteCall(id int64) (string, error) {
	var p string
	if err := d.db.QueryRow(`SELECT path FROM calls WHERE id=?`, id).Scan(&p); err != nil {
		return "", err
	}
	_, err := d.db.Exec(`DELETE FROM calls WHERE id=?`, id)
	return p, err
}

// OlderThan returns calls started before cutoff (for retention cleanup).
func (d *DB) OlderThan(cutoff time.Time) ([]*Call, error) {
	rows, err := d.db.Query(`SELECT `+callCols+` FROM calls WHERE started_at < ? ORDER BY id LIMIT 500`, FormatTime(cutoff))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Call
	for rows.Next() {
		c, err := scanCall(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AllTranscripts iterates over done calls with a transcript (dictionary re-apply).
func (d *DB) AllTranscripts(fn func(id int64, text string) error) error {
	rows, err := d.db.Query(`SELECT id, transcript FROM calls WHERE transcript IS NOT NULL AND transcript <> ''`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type row struct {
		id int64
		t  string
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.t); err != nil {
			return err
		}
		all = append(all, r)
	}
	rows.Close()
	for _, r := range all {
		if err := fn(r.id, r.t); err != nil {
			return err
		}
	}
	return nil
}

// SetTranscriptText replaces only the text.
func (d *DB) SetTranscriptText(id int64, text string) error {
	_, err := d.db.Exec(`UPDATE calls SET transcript=? WHERE id=?`, text, id)
	return err
}

// InsertNotifyLog appends a notification result.
func (d *DB) InsertNotifyLog(l NotifyLog) error {
	_, err := d.db.Exec(`INSERT INTO notify_log(call_id, webhook_id, sent_at, status, http_status, error) VALUES(?,?,?,?,?,?)`,
		l.CallID, l.WebhookID, l.SentAt, l.Status, l.HTTPStatus, l.Error)
	return err
}

// RecentNotifyLog returns the latest n rows.
func (d *DB) RecentNotifyLog(n int) ([]NotifyLog, error) {
	rows, err := d.db.Query(`SELECT id, call_id, webhook_id, sent_at, status, http_status, error FROM notify_log ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotifyLog{}
	for rows.Next() {
		var l NotifyLog
		if err := rows.Scan(&l.ID, &l.CallID, &l.WebhookID, &l.SentAt, &l.Status, &l.HTTPStatus, &l.Error); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// TotalBytesHint returns the sum of durations (seconds) — used by /api/disk as
// a cheap approximation is not enough, so callers walk the directory instead.
func (d *DB) CountCalls() (int, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM calls`).Scan(&n)
	return n, err
}
