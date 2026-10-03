package detect

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ahenager/nearby-fire-alert/internal/config"
	"github.com/ahenager/nearby-fire-alert/internal/geo"
	"github.com/ahenager/nearby-fire-alert/internal/logx"
	"github.com/ahenager/nearby-fire-alert/internal/openclaw"
	"github.com/ahenager/nearby-fire-alert/internal/providerapi"
	"github.com/ahenager/nearby-fire-alert/internal/store"
)

type staticProvider struct {
	name      string
	events    []providerapi.Event
	enabled   bool
	interval  time.Duration
	confFloor float64
}

func (p *staticProvider) Name() string { return p.name }
func (p *staticProvider) Configure(map[string]any, func(string) (string, bool)) error {
	return nil
}
func (p *staticProvider) Fetch(context.Context, time.Time) ([]providerapi.Event, error) {
	return p.events, nil
}
func (p *staticProvider) Enabled() bool { return p.enabled }
func (p *staticProvider) Interval() time.Duration {
	if p.interval > 0 {
		return p.interval
	}
	return time.Minute
}
func (p *staticProvider) ConfidenceFloor() float64 { return p.confFloor }

var testProvider = &staticProvider{name: "static", enabled: true, confFloor: 75}

type postCapture struct {
	mu    sync.Mutex
	posts int
	body  []byte
}

func (c *postCapture) handler(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.posts++
	c.body = b
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true,"runId":"r1"}`))
}

func baseConfig() *config.Config {
	return &config.Config{
		Geofence: config.GeofenceConfig{HomeLat: 35, HomeLon: -97, RadiusMiles: 5, BufferMiles: 25},
		Runtime: config.RuntimeConfig{
			EventTTLHours:          24,
			LogLevel:               "debug",
			EscalationFactor:       0.20,
			EscalationPersistHours: 24,
			EscalationCooldownMin:  60,
		},
		OpenClaw: config.OpenClawConfig{HookURL: "http://unused/hooks/fire", HookToken: "tok"},
	}
}

func newStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.New("sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newEngine(t *testing.T, cfg *config.Config, st store.Store, dryRun bool) *Engine {
	t.Helper()
	if cfg == nil {
		cfg = baseConfig()
	}
	return New(Options{
		Config:    cfg,
		Store:     st,
		Providers: []providerapi.Provider{testProvider},
		Notifier:  openclaw.New(cfg.OpenClaw, logx.Discard()),
		Logger:    logx.Discard(),
		DryRun:    dryRun,
	})
}

func newEngineWithCap(t *testing.T, events []providerapi.Event) (*Engine, store.Store, *postCapture) {
	t.Helper()
	cap := &postCapture{}
	srv := httptest.NewServer(http.HandlerFunc(cap.handler))
	t.Cleanup(srv.Close)
	cfg := baseConfig()
	cfg.OpenClaw.HookURL = srv.URL + "/hooks/fire"
	st := newStore(t)
	return newEngine(t, cfg, st, false), st, cap
}

func hotspot(id string, lat, lon, conf float64) providerapi.Event {
	return providerapi.Event{
		EventID: id, Kind: "hotspot", Source: "viirs-snpp",
		DetectedAt: time.Now().UTC(), Latitude: lat, Longitude: lon,
		Confidence: conf, FRP: 10, BrightnessTempK: 320,
	}
}

func TestNewHotspotPostsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	e, _, cap := newEngineWithCap(t, nil)
	ev := hotspot("h1", 35.01, -97.0, 95)
	e.evaluate(ctx, testProvider, ev)
	e.evaluate(ctx, testProvider, ev)
	e.evaluate(ctx, testProvider, ev)
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.posts != 1 {
		t.Fatalf("expected exactly 1 post, got %d", cap.posts)
	}
}

func TestOutsideRadiusNeverPosted(t *testing.T) {
	e, _, cap := newEngineWithCap(t, nil)
	e.evaluate(context.Background(), testProvider, hotspot("far", 36.5, -97.0, 95))
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.posts != 0 {
		t.Fatalf("expected 0 posts, got %d", cap.posts)
	}
}

func TestBelowConfidenceNeverPosted(t *testing.T) {
	e, _, cap := newEngineWithCap(t, nil)
	e.evaluate(context.Background(), testProvider, hotspot("low", 35.01, -97.0, 50))
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.posts != 0 {
		t.Fatalf("expected 0 posts, got %d", cap.posts)
	}
}

func TestPerimeterPostsEvenIfLowConfidence(t *testing.T) {
	ev := providerapi.Event{
		EventID: "nifc:p1", Kind: "perimeter", Source: "nifc",
		DetectedAt: time.Now().UTC(), Latitude: 35.5, Longitude: -96.5,
		Confidence: 40, // low satellite confidence, must still alert
	}
	poly := geo.Polygon{
		{Latitude: 35.0, Longitude: -97.0},
		{Latitude: 35.0, Longitude: -96.0},
		{Latitude: 36.0, Longitude: -96.0},
		{Latitude: 36.0, Longitude: -97.0},
	}
	ev.Perimeter = &poly
	e, _, cap := newEngineWithCap(t, nil)
	e.evaluate(context.Background(), testProvider, ev)
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.posts != 1 {
		t.Fatalf("expected perimeter to post despite low confidence, got %d posts", cap.posts)
	}
}

func TestRestartDoesNotRealert(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	now := time.Now().UTC().Add(-time.Hour)
	if err := st.UpsertKnownEvent(ctx, store.KnownEvent{
		EventID: "h1", Kind: "hotspot", Source: "viirs-snpp",
		FirstSeen: now, LastSeen: now, LastConfidence: 95, LastFRP: 10, Delivered: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkDelivered(ctx, "h1", now); err != nil {
		t.Fatal(err)
	}
	cap := &postCapture{}
	srv := httptest.NewServer(http.HandlerFunc(cap.handler))
	defer srv.Close()
	cfg := baseConfig()
	cfg.OpenClaw.HookURL = srv.URL + "/hooks/fire"
	e := newEngine(t, cfg, st, false)
	e.evaluate(ctx, testProvider, hotspot("h1", 35.01, -97.0, 95))
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.posts != 0 {
		t.Fatalf("restart must not re-alert known event, got %d posts", cap.posts)
	}
}

func TestEscalationOnConfidenceRise(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	now := time.Now().UTC().Add(-time.Hour)
	if err := st.UpsertKnownEvent(ctx, store.KnownEvent{
		EventID: "h1", Kind: "hotspot", Source: "viirs-snpp",
		FirstSeen: now, LastSeen: now, LastConfidence: 50, LastFRP: 10, Delivered: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkDelivered(ctx, "h1", now); err != nil {
		t.Fatal(err)
	}
	cap := &postCapture{}
	srv := httptest.NewServer(http.HandlerFunc(cap.handler))
	defer srv.Close()
	cfg := baseConfig()
	cfg.OpenClaw.HookURL = srv.URL + "/hooks/fire"
	e := newEngine(t, cfg, st, false)
	e.evaluate(ctx, testProvider, hotspot("h1", 35.01, -97.0, 80))
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.posts != 1 {
		t.Fatalf("expected escalation re-alert, got %d posts", cap.posts)
	}
}

func TestWatchZoneNotAlerted(t *testing.T) {
	e, _, cap := newEngineWithCap(t, nil)
	e.evaluate(context.Background(), testProvider, hotspot("watch", 35.14, -97.0, 95))
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.posts != 0 {
		t.Fatalf("expected watch zone not alerted, got %d posts", cap.posts)
	}
}

func TestDryRunDoesNotSend(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	e := newEngine(t, baseConfig(), st, true)
	e.evaluate(ctx, testProvider, hotspot("h1", 35.01, -97.0, 95))
	ke, ok, _ := st.GetKnownEvent(ctx, "h1")
	if ok && ke.Delivered {
		t.Fatal("dry-run must not mark delivered")
	}
}
