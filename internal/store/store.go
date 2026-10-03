// Package store defines the durable persistence interface used by the service
// and provides two implementations: sqlite (default, zero-config) and postgres.
//
// The store persists known events (for dedup and restart-safety), detection
// history, and health records. It is idempotent: recording the same event id
// repeatedly produces the same state. All queries are parameterized.
package store

import (
	"context"
	"time"
)

// KnownEvent is a fire event the service has observed, used for dedupe and
// restart-safety.
type KnownEvent struct {
	EventID        string
	Kind           string
	Source         string
	FirstSeen      time.Time
	LastSeen       time.Time
	LastConfidence float64
	LastFRP        float64
	Delivered      bool
	DeliveredAt    *time.Time
}

// Detection records the outcome of evaluating an event: whether it was
// alerted, watched, or skipped, plus the reason.
type Detection struct {
	At        time.Time
	Source    string
	EventID   string
	Decision  string // alert | watch | skip
	Reason    string
	Latitude  float64
	Longitude float64
}

// Health records a periodic or ad-hoc health/run marker.
type Health struct {
	RunID  string
	At     time.Time
	Level  string
	Detail string
}

// Store is the persistence boundary. Implementations must be durable across
// restarts and idempotent for known events.
type Store interface {
	// Migrate applies schema migrations idempotently.
	Migrate(ctx context.Context) error

	// GetKnownEvent returns the known event for eventID. ok is false when the
	// event is not (yet) known.
	GetKnownEvent(ctx context.Context, eventID string) (KnownEvent, bool, error)

	// UpsertKnownEvent creates or updates a known event keyed by EventID.
	UpsertKnownEvent(ctx context.Context, ev KnownEvent) error

	// MarkDelivered marks an event as delivered at the given time.
	MarkDelivered(ctx context.Context, eventID string, at time.Time) error

	// DeleteExpiredEvents removes events whose last_seen is before the cutoff.
	// Returns the number of rows removed.
	DeleteExpiredEvents(ctx context.Context, before time.Time) (int64, error)

	// RecordDetection appends a detection outcome.
	RecordDetection(ctx context.Context, d Detection) error

	// RecordHealth appends a health marker.
	RecordHealth(ctx context.Context, h Health) error

	// Close releases underlying resources.
	Close() error
}

// New opens a store for the given driver ("sqlite" or "postgres") and
// configuration. For sqlite, arg is the file path. For postgres, arg is the
// DSN/URL. The interface is implemented by the driver packages.
func New(driver, arg string) (Store, error) {
	switch driver {
	case "sqlite":
		return newSQLite(arg)
	case "postgres":
		return newPostgres(arg)
	default:
		return nil, &UnsupportedDriverError{Driver: driver}
	}
}

// UnsupportedDriverError is returned when an unknown db.driver is configured.
type UnsupportedDriverError struct {
	Driver string
}

func (e *UnsupportedDriverError) Error() string {
	return "unsupported db driver: " + e.Driver
}
