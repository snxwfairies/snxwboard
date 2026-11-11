// Package store is the durable append-only event log.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	_ "modernc.org/sqlite"
)

// Event is one row of the log. Seq is assigned by SQLite on insert.
type Event struct {
	Seq     int64           `json:"seq"`
	TS      int64           `json:"ts"`
	Source  string          `json:"source"`
	Session string          `json:"session"`
	Kind    string          `json:"kind"`
	Body    json.RawMessage `json:"body"`
}

// Store owns the SQLite handle. A single connection keeps writers and replayers
// from fighting over the lock; batches are small so serialization is cheap.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS events (
	seq     INTEGER PRIMARY KEY AUTOINCREMENT,
	ts      INTEGER NOT NULL,
	source  TEXT    NOT NULL,
	session TEXT    NOT NULL DEFAULT '',
	kind    TEXT    NOT NULL,
	body    BLOB
);
CREATE INDEX IF NOT EXISTS events_ts ON events(ts);
CREATE TABLE IF NOT EXISTS sessions (
	id          TEXT PRIMARY KEY,
	data        BLOB    NOT NULL,
	updated_seq INTEGER NOT NULL DEFAULT 0
);
`

// SessionRow is one folded session as persisted: its JSON state and the seq of
// the last event folded into it.
type SessionRow struct {
	ID         string
	Data       json.RawMessage
	UpdatedSeq int64
}

// FoldFunc runs inside the write transaction after Seq assignment and returns
// the folded sessions to upsert next to the events.
type FoldFunc func(evs []Event) []SessionRow

// queryer is the subset of *sql.DB and *sql.Tx used by the read helpers.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Open opens or creates the event log at path and enables WAL.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// InsertBatch appends events in one transaction and fills in their Seq fields.
func (s *Store) InsertBatch(ctx context.Context, evs []Event) error {
	return s.WriteBatch(ctx, evs, nil)
}

// WriteBatch appends events and upserts the sessions returned by fold in the
// same transaction, so folded state never runs ahead of the log.
func (s *Store) WriteBatch(ctx context.Context, evs []Event, fold FoldFunc) error {
	if len(evs) == 0 && fold == nil {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO events(ts, source, session, kind, body) VALUES(?,?,?,?,?)`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	for i := range evs {
		res, err := stmt.ExecContext(ctx, evs[i].TS, evs[i].Source, evs[i].Session, evs[i].Kind, []byte(evs[i].Body))
		if err != nil {
			return fmt.Errorf("insert: %w", err)
		}
		seq, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("last insert id: %w", err)
		}
		evs[i].Seq = seq
	}

	if fold != nil {
		for _, row := range fold(evs) {
			if row.ID == "" {
				continue
			}
			_, err := tx.ExecContext(ctx, `
				INSERT INTO sessions(id, data, updated_seq) VALUES(?,?,?)
				ON CONFLICT(id) DO UPDATE SET data=excluded.data, updated_seq=excluded.updated_seq`,
				row.ID, []byte(row.Data), row.UpdatedSeq)
			if err != nil {
				return fmt.Errorf("upsert session %s: %w", row.ID, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// LoadSessions returns every persisted session, ordered by id.
func (s *Store) LoadSessions(ctx context.Context) ([]SessionRow, error) {
	return querySessions(ctx, s.db)
}

// SnapshotSessions returns the highest event seq and the folded sessions in one
// read transaction, so a client can resume from that seq without a gap.
func (s *Store) SnapshotSessions(ctx context.Context) (int64, []SessionRow, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	var seq int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&seq); err != nil {
		return 0, nil, fmt.Errorf("max seq: %w", err)
	}
	rows, err := querySessions(ctx, tx)
	if err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, fmt.Errorf("commit: %w", err)
	}
	return seq, rows, nil
}

func querySessions(ctx context.Context, q queryer) ([]SessionRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, data, updated_seq FROM sessions ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	var out []SessionRow
	for rows.Next() {
		var (
			row  SessionRow
			data []byte
		)
		if err := rows.Scan(&row.ID, &data, &row.UpdatedSeq); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		row.Data = json.RawMessage(data)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("session rows: %w", err)
	}
	return out, nil
}

// QueryAfter returns up to limit events with Seq > after, oldest first.
func (s *Store) QueryAfter(ctx context.Context, after int64, limit int) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, ts, source, session, kind, body FROM events WHERE seq > ? ORDER BY seq LIMIT ?`,
		after, limit)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var (
			ev   Event
			body []byte
		)
		if err := rows.Scan(&ev.Seq, &ev.TS, &ev.Source, &ev.Session, &ev.Kind, &body); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		ev.Body = json.RawMessage(body)
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}
