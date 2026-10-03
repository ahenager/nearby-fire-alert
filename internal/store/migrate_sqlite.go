package store

import "context"

// sqliteMigrations is the ordered list of DDL statements for the SQLite backend.
var sqliteMigrations = []string{
	`CREATE TABLE IF NOT EXISTS known_events (
		event_id        TEXT PRIMARY KEY,
		kind            TEXT NOT NULL,
		source          TEXT NOT NULL,
		first_seen      DATETIME NOT NULL,
		last_seen       DATETIME NOT NULL,
		last_confidence REAL NOT NULL,
		last_frp        REAL NOT NULL,
		delivered       INTEGER NOT NULL DEFAULT 0,
		delivered_at    DATETIME
	)`,
	`CREATE INDEX IF NOT EXISTS idx_known_events_last_seen ON known_events(last_seen)`,
	`CREATE TABLE IF NOT EXISTS detections (
		id       INTEGER PRIMARY KEY AUTOINCREMENT,
		at       DATETIME NOT NULL,
		source   TEXT NOT NULL,
		event_id TEXT,
		decision TEXT NOT NULL,
		reason   TEXT NOT NULL,
		latitude REAL,
		longitude REAL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_detections_at ON detections(at)`,
	`CREATE TABLE IF NOT EXISTS health (
		id      INTEGER PRIMARY KEY AUTOINCREMENT,
		run_id  TEXT,
		at      DATETIME NOT NULL,
		level   TEXT,
		detail  TEXT
	)`,
}

// migrateSQLite applies the sqlite schema migrations.
func migrateSQLite(ctx context.Context, s *sqliteStore) error {
	for _, stmt := range sqliteMigrations {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
