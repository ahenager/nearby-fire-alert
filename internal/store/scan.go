package store

import (
	"database/sql"
)

// rowScanner matches the signature of *sql.Row.Scan and used for shared
// scanning logic across drivers.
type rowScanner func(dest ...any) error

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// scanKnownEvent scans a known_events row in the canonical column order:
// event_id, kind, source, first_seen, last_seen, last_confidence, last_frp,
// delivered, delivered_at.
func scanKnownEvent(scan rowScanner) (KnownEvent, error) {
	var ev KnownEvent
	var delivered int
	var deliveredAt sql.NullTime
	err := scan(
		&ev.EventID, &ev.Kind, &ev.Source,
		&ev.FirstSeen, &ev.LastSeen,
		&ev.LastConfidence, &ev.LastFRP,
		&delivered, &deliveredAt,
	)
	if err != nil {
		return KnownEvent{}, err
	}
	ev.Delivered = delivered != 0
	if deliveredAt.Valid {
		ev.DeliveredAt = &deliveredAt.Time
	}
	return ev, nil
}
