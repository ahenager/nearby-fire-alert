package store

import "context"

// postgresMigrations is the ordered list of DDL statements for the Postgres
// backend. The table shape mirrors the sqlite schema; auto-increment columns
// use SERIAL and timestamps use TIMESTAMPTZ.
var postgresMigrations = []string{
	`CREATE TABLE IF NOT EXISTS known_events (
		event_id        TEXT PRIMARY KEY,
		kind            TEXT NOT NULL,
		source          TEXT NOT NULL,
		first_seen      TIMESTAMPTZ NOT NULL,
		last_seen       TIMESTAMPTZ NOT NULL,
		last_confidence DOUBLE PRECISION NOT NULL,
		last_frp        DOUBLE PRECISION NOT NULL,
		delivered       INTEGER NOT NULL DEFAULT 0,
		delivered_at    TIMESTAMPTZ
	)`,
	`CREATE INDEX IF NOT EXISTS idx_known_events_last_seen ON known_events(last_seen)`,
	`CREATE TABLE IF NOT EXISTS detections (
		id       SERIAL PRIMARY KEY,
		at       TIMESTAMPTZ NOT NULL,
		source   TEXT NOT NULL,
		event_id TEXT,
		decision TEXT NOT NULL,
		reason   TEXT NOT NULL,
		latitude DOUBLE PRECISION,
		longitude DOUBLE PRECISION
	)`,
	`CREATE INDEX IF NOT EXISTS idx_detections_at ON detections(at)`,
	`CREATE TABLE IF NOT EXISTS health (
		id      SERIAL PRIMARY KEY,
		run_id  TEXT,
		at      TIMESTAMPTZ NOT NULL,
		level   TEXT,
		detail  TEXT
	)`,
}

// migratePostgres applies the postgres schema migrations.
func migratePostgres(ctx context.Context, s *postgresStore) error {
	for _, stmt := range postgresMigrations {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
