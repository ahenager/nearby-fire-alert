package openclaw

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ahenager/nearby-fire-alert/internal/config"
	"github.com/ahenager/nearby-fire-alert/internal/logx"
	"github.com/ahenager/nearby-fire-alert/internal/providerapi"
)

func testCfg() config.OpenClawConfig {
	return config.OpenClawConfig{
		HookURL:        "http://example.invalid/hooks/fire",
		HookToken:      "tok123",
		AgentID:        "main",
		Channel:        "signal",
		To:             "+15550100400",
		TimeoutSeconds: 5,
	}
}

func TestBuildPayloadSchema(t *testing.T) {
	ev := providerapi.Event{
		EventID: "firms:viirs-snpp:35.1000,-96.9000:2023-01-01",
		Kind:    "hotspot", Source: "viirs-snpp",
		DetectedAt: time.Date(2023, 1, 1, 15, 30, 0, 0, time.UTC),
		Latitude:   35.1, Longitude: -96.9,
		DistanceMiles: 2.3, Confidence: 95, FRP: 25, BrightnessTempK: 350.1,
		IncidentName: "Test Fire",
	}
	p := BuildPayload(ev, Summary(ev), testCfg())
	raw, _ := json.Marshal(p)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	e, ok := m["event"].(map[string]any)
	if !ok {
		t.Fatalf("missing event object: %v", m)
	}
	if e["event_id"] != ev.EventID || e["kind"] != "hotspot" || e["source"] != "viirs-snpp" {
		t.Fatalf("bad top-level event fields: %v", e)
	}
	if e["frp"] != 25.0 || e["brightness_temp_k"] != 350.1 {
		t.Fatalf("bad numeric nullable fields: %v", e)
	}
	if _, ok := m["summary"].(string); !ok {
		t.Fatalf("missing summary")
	}
	// For /hooks/fire (not agent), routing must be omitted.
	if _, has := m["channel"]; has {
		t.Fatalf("routing should be omitted for /hooks/fire")
	}
}

func TestBuildPayloadRoutingForAgentHook(t *testing.T) {
	cfg := testCfg()
	cfg.HookURL = "http://example.invalid/hooks/agent"
	ev := providerapi.Event{EventID: "x", Kind: "hotspot", Source: "s", DetectedAt: time.Now().UTC()}
	p := BuildPayload(ev, "summary", cfg)
	if p.Channel != "signal" || p.To != "+15550100400" || p.AccountID != "" {
		t.Fatalf("routing not included for agent hook: %+v", p)
	}
}

type capture struct {
	mu          sync.Mutex
	posts       int
	lastHeaders http.Header
	lastBody    []byte
	status      int
}

func (c *capture) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.posts++
	c.lastHeaders = r.Header.Clone()
	c.lastBody = body
	c.mu.Unlock()
	if c.status != 0 {
		w.WriteHeader(c.status)
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"runId":"run-123"}`))
	}
}

func newServer(t *testing.T, cap *capture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(cap.handler))
}

func TestPostAdmissionHeadersBody(t *testing.T) {
	cap := &capture{}
	srv := newServer(t, cap)
	defer srv.Close()
	cfg := testCfg()
	cfg.HookURL = srv.URL + "/hooks/fire"

	c := New(cfg, logx.Discard())
	ev := providerapi.Event{EventID: "evt-1", Kind: "hotspot", Source: "modis", DetectedAt: time.Now().UTC(), Latitude: 1, Longitude: 2, Confidence: 80}
	payload := BuildPayload(ev, "summary here", cfg)
	res, err := c.Post(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Delivered || res.RunID != "run-123" {
		t.Fatalf("unexpected result: %+v", res)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.posts != 1 {
		t.Fatalf("expected 1 post, got %d", cap.posts)
	}
	if g := cap.lastHeaders.Get("Content-Type"); g != "application/json" {
		t.Fatalf("bad content-type: %s", g)
	}
	if g := cap.lastHeaders.Get("Authorization"); !strings.Contains(g, "Bearer tok123") {
		t.Fatalf("bad auth: %s", g)
	}
	if g := cap.lastHeaders.Get("Idempotency-Key"); g != "evt-1" {
		t.Fatalf("bad idempotency key: %s", g)
	}
	var got Payload
	if err := json.Unmarshal(cap.lastBody, &got); err != nil {
		t.Fatal(err)
	}
	if got.Event.EventID != "evt-1" || got.Summary != "summary here" {
		t.Fatalf("body mismatch: %+v", got)
	}
}

func TestPostRetriesOnTransientThenSucceeds(t *testing.T) {
	var attempts int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	cfg := testCfg()
	cfg.HookURL = srv.URL + "/hooks/fire"
	c := New(cfg, logx.Discard())
	payload := BuildPayload(providerapi.Event{EventID: "e", Kind: "hotspot", Source: "s", DetectedAt: time.Now().UTC()}, "s", cfg)
	res, err := c.Post(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Delivered {
		t.Fatal("expected delivery after retries")
	}
	mu.Lock()
	if attempts < 3 {
		t.Fatalf("expected at least 3 attempts, got %d", attempts)
	}
	mu.Unlock()
}

func TestPostDoesNotRetryOn2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	cfg := testCfg()
	cfg.HookURL = srv.URL + "/hooks/fire"
	c := New(cfg, logx.Discard())
	payload := BuildPayload(providerapi.Event{EventID: "e2", Kind: "hotspot", Source: "s", DetectedAt: time.Now().UTC()}, "s", cfg)
	if _, err := c.Post(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
}

func TestPostFailsAfterRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	cfg := testCfg()
	cfg.HookURL = srv.URL + "/hooks/fire"
	c := New(cfg, logx.Discard())
	payload := BuildPayload(providerapi.Event{EventID: "e3", Kind: "hotspot", Source: "s", DetectedAt: time.Now().UTC()}, "s", cfg)
	if _, err := c.Post(context.Background(), payload); err == nil {
		t.Fatal("expected error after exhausting retries")
	}
}
