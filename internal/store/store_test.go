package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestSQLite(t *testing.T) Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := New("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newTestPostgres(t *testing.T) Store {
	t.Helper()
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN not set; skipping postgres store test")
	}
	s, err := New("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStoreUpsertIdempotent(t *testing.T) {
	for _, makeStore := range []func(*testing.T) Store{newTestSQLite, newTestPostgres} {
		s := makeStore(t)
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Second)

		ev := KnownEvent{
			EventID: "abc-123", Kind: "hotspot", Source: "viirs-snpp",
			FirstSeen: now, LastSeen: now, LastConfidence: 80, LastFRP: 30,
		}
		if err := s.UpsertKnownEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
		// Second upsert with updated state must be idempotent.
		ev2 := ev
		ev2.LastConfidence = 92
		ev2.LastFRP = 85
		if err := s.UpsertKnownEvent(ctx, ev2); err != nil {
			t.Fatal(err)
		}

		got, ok, err := s.GetKnownEvent(ctx, "abc-123")
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatal("event should be known")
		}
		if got.FirstSeen != now {
			t.Fatalf("first_seen should persist: %v vs %v", got.FirstSeen, now)
		}
		if got.LastConfidence != 92 || got.LastFRP != 85 {
			t.Fatalf("last fields not updated: %+v", got)
		}
		if got.Delivered {
			t.Fatal("should not be delivered yet")
		}
	}
}

func TestStoreMarkDelivered(t *testing.T) {
	for _, makeStore := range []func(*testing.T) Store{newTestSQLite, newTestPostgres} {
		s := makeStore(t)
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Second)
		ev := KnownEvent{EventID: "id-1", Kind: "hotspot", Source: "modis", FirstSeen: now, LastSeen: now}
		if err := s.UpsertKnownEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkDelivered(ctx, "id-1", now); err != nil {
			t.Fatal(err)
		}
		got, ok, _ := s.GetKnownEvent(ctx, "id-1")
		if !ok || !got.Delivered || got.DeliveredAt == nil {
			t.Fatalf("expected delivered event, got %+v", got)
		}
	}
}

func TestStoreDeleteExpired(t *testing.T) {
	for _, makeStore := range []func(*testing.T) Store{newTestSQLite, newTestPostgres} {
		s := makeStore(t)
		ctx := context.Background()
		old := time.Now().Add(-72 * time.Hour)
		recent := time.Now().UTC().Truncate(time.Second)
		_ = s.UpsertKnownEvent(ctx, KnownEvent{EventID: "old", Kind: "hotspot", Source: "x", FirstSeen: old, LastSeen: old})
		_ = s.UpsertKnownEvent(ctx, KnownEvent{EventID: "new", Kind: "hotspot", Source: "x", FirstSeen: recent, LastSeen: recent})

		n, err := s.DeleteExpiredEvents(ctx, time.Now().Add(-48*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("expected 1 expired deletion, got %d", n)
		}
		if _, ok, _ := s.GetKnownEvent(ctx, "old"); ok {
			t.Fatal("old event should be gone")
		}
		if _, ok, _ := s.GetKnownEvent(ctx, "new"); !ok {
			t.Fatal("new event should remain")
		}
	}
}

func TestStoreDetectionAndHealth(t *testing.T) {
	for _, makeStore := range []func(*testing.T) Store{newTestSQLite, newTestPostgres} {
		s := makeStore(t)
		ctx := context.Background()
		now := time.Now().UTC()
		if err := s.RecordDetection(ctx, Detection{At: now, Source: "firms", EventID: "e1", Decision: "alert", Reason: "in radius"}); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordHealth(ctx, Health{RunID: "run-1", At: now, Level: "info", Detail: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestStoreResurrectAfterReopen ensures state survives a close/reopen cycle
// (equivalent to a process restart) and the event is still known.
func TestStoreSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	s, err := New("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_ = s.Migrate(ctx)
	now := time.Now().UTC()
	_ = s.UpsertKnownEvent(ctx, KnownEvent{EventID: "persist", Kind: "hotspot", Source: "x", FirstSeen: now, LastSeen: now})
	_ = s.Close()

	s2, err := New("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	_ = s2.Migrate(ctx)
	if _, ok, _ := s2.GetKnownEvent(ctx, "persist"); !ok {
		t.Fatal("event should survive reopen")
	}
}

func TestUnsupportedDriver(t *testing.T) {
	_, err := New("mysql", "")
	if err == nil {
		t.Fatal("expected error for unsupported driver")
	}
}
