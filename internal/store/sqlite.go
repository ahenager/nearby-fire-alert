package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, no cgo required
)

type sqliteStore struct {
	db *sql.DB
}

func newSQLite(path string) (*sqliteStore, error) {
	// Journal mode WAL + busy timeout improve concurrency/durability for a
	// monitoring daemon reading/writing a single file.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// Ensure the file/directory are writable eagerly.
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite ping: %w", err)
	}
	return &sqliteStore{db: db}, nil
}

func (s *sqliteStore) Migrate(ctx context.Context) error {
	return migrateSQLite(ctx, s)
}

func (s *sqliteStore) GetKnownEvent(ctx context.Context, eventID string) (KnownEvent, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT event_id, kind, source, first_seen, last_seen, last_confidence, last_frp, delivered, delivered_at
		 FROM known_events WHERE event_id = ?`, eventID)
	ev, err := scanKnownEvent(row.Scan)
	if err == sql.ErrNoRows {
		return KnownEvent{}, false, nil
	}
	if err != nil {
		return KnownEvent{}, false, err
	}
	return ev, true, nil
}

func (s *sqliteStore) UpsertKnownEvent(ctx context.Context, ev KnownEvent) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO known_events
			(event_id, kind, source, first_seen, last_seen, last_confidence, last_frp, delivered, delivered_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(event_id) DO UPDATE SET
			kind = excluded.kind,
			source = excluded.source,
			last_seen = excluded.last_seen,
			last_confidence = excluded.last_confidence,
			last_frp = excluded.last_frp`,
		ev.EventID, ev.Kind, ev.Source, ev.FirstSeen, ev.LastSeen,
		ev.LastConfidence, ev.LastFRP, boolToInt(ev.Delivered), ev.DeliveredAt)
	return err
}

func (s *sqliteStore) MarkDelivered(ctx context.Context, eventID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE known_events SET delivered = 1, delivered_at = ? WHERE event_id = ?`, at, eventID)
	return err
}

func (s *sqliteStore) DeleteExpiredEvents(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM known_events WHERE last_seen < ?`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *sqliteStore) RecordDetection(ctx context.Context, d Detection) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO detections (at, source, event_id, decision, reason, latitude, longitude)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.At, d.Source, d.EventID, d.Decision, d.Reason, d.Latitude, d.Longitude)
	return err
}

func (s *sqliteStore) RecordHealth(ctx context.Context, h Health) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO health (run_id, at, level, detail) VALUES (?, ?, ?, ?)`,
		h.RunID, h.At, h.Level, h.Detail)
	return err
}

func (s *sqliteStore) Close() error {
	return s.db.Close()
}
