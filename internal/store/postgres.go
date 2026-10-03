package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

type postgresStore struct {
	db *sql.DB
}

func newPostgres(dsn string) (*postgresStore, error) {
	if dsn == "" {
		return nil, fmt.Errorf("postgres dsn must be set when driver=postgres")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres ping: %w", err)
	}
	return &postgresStore{db: db}, nil
}

func (s *postgresStore) Migrate(ctx context.Context) error {
	return migratePostgres(ctx, s)
}

func (s *postgresStore) GetKnownEvent(ctx context.Context, eventID string) (KnownEvent, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT event_id, kind, source, first_seen, last_seen, last_confidence, last_frp, delivered, delivered_at
		 FROM known_events WHERE event_id = $1`, eventID)
	ev, err := scanKnownEvent(row.Scan)
	if err == sql.ErrNoRows {
		return KnownEvent{}, false, nil
	}
	if err != nil {
		return KnownEvent{}, false, err
	}
	return ev, true, nil
}

func (s *postgresStore) UpsertKnownEvent(ctx context.Context, ev KnownEvent) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO known_events
			(event_id, kind, source, first_seen, last_seen, last_confidence, last_frp, delivered, delivered_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT(event_id) DO UPDATE SET
			kind = EXCLUDED.kind,
			source = EXCLUDED.source,
			last_seen = EXCLUDED.last_seen,
			last_confidence = EXCLUDED.last_confidence,
			last_frp = EXCLUDED.last_frp`,
		ev.EventID, ev.Kind, ev.Source, ev.FirstSeen, ev.LastSeen,
		ev.LastConfidence, ev.LastFRP, boolToInt(ev.Delivered), ev.DeliveredAt)
	return err
}

func (s *postgresStore) MarkDelivered(ctx context.Context, eventID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE known_events SET delivered = 1, delivered_at = $1 WHERE event_id = $2`, at, eventID)
	return err
}

func (s *postgresStore) DeleteExpiredEvents(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM known_events WHERE last_seen < $1`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *postgresStore) RecordDetection(ctx context.Context, d Detection) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO detections (at, source, event_id, decision, reason, latitude, longitude)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		d.At, d.Source, d.EventID, d.Decision, d.Reason, d.Latitude, d.Longitude)
	return err
}

func (s *postgresStore) RecordHealth(ctx context.Context, h Health) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO health (run_id, at, level, detail) VALUES ($1, $2, $3, $4)`,
		h.RunID, h.At, h.Level, h.Detail)
	return err
}

func (s *postgresStore) Close() error {
	return s.db.Close()
}
