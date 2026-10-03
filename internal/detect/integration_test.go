package detect

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ahenager/nearby-fire-alert/internal/config"
	"github.com/ahenager/nearby-fire-alert/internal/logx"
	"github.com/ahenager/nearby-fire-alert/internal/openclaw"
	"github.com/ahenager/nearby-fire-alert/internal/providerapi"
	"github.com/ahenager/nearby-fire-alert/internal/providers/firms"
	"github.com/ahenager/nearby-fire-alert/internal/store"
)

// TestIntegrationFirmsToOpenClaw is an end-to-end test with a real FIRMS
// provider, a real SQLite store, and a real (mock) OpenClaw hook. It asserts
// the acceptance criteria: a FIRMS hotspot meeting the confidence floor within
// the alert radius produces EXACTLY ONE post, and a second poll does not post
// again.
func TestIntegrationFirmsToOpenClaw(t *testing.T) {
	// Mock FIRMS CSV endpoint.
	firmsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/testkey/") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/csv")
		fmt.Fprint(w, "latitude,longitude,bright_ti4,scan,track,acq_date,acq_time,satellite,confidence,version,bright_ti5,frp,daynight\n"+
			"35.01,-97.0,350.1,1,1,2023-01-01,1530,VIIRS_SNPP,95,1,300.1,25.0,D\n")
	}))
	defer firmsSrv.Close()

	var posts atomic.Int32
	ocSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"runId":"r-int"}`)
	}))
	defer ocSrv.Close()

	cfg := &config.Config{
		Geofence: config.GeofenceConfig{HomeLat: 35, HomeLon: -97, RadiusMiles: 5, BufferMiles: 25},
		Runtime: config.RuntimeConfig{
			EventTTLHours: 24, LogLevel: "info",
			EscalationFactor: 0.20, EscalationPersistHours: 24, EscalationCooldownMin: 60,
		},
		OpenClaw: config.OpenClawConfig{HookURL: ocSrv.URL + "/hooks/fire", HookToken: "tok", TimeoutSeconds: 5},
	}

	env := map[string]string{
		"FIRMS_API_URL":          firmsSrv.URL,
		"FIRMS_MAP_KEY":          "testkey",
		"FIRMS_ENABLED":          "true",
		"FIRMS_SENSORS":          "VIIRS_SNPP",
		"FIRMS_INTERVAL_SECONDS": "300",
		"FIRMS_MIN_CONFIDENCE":   "75",
	}
	get := func(k string) (string, bool) { v, ok := env[k]; return v, ok }

	fp := firms.New()
	if err := fp.Configure(nil, get); err != nil {
		t.Fatal(err)
	}

	st, err := store.New("sqlite", filepath.Join(t.TempDir(), "it.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	e := New(Options{
		Config:    cfg,
		Store:     st,
		Providers: []providerapi.Provider{fp},
		Notifier:  openclaw.New(cfg.OpenClaw, logx.Discard()),
		Logger:    logx.Discard(),
	})

	ctx := context.Background()
	if err := e.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := posts.Load(); got != 1 {
		t.Fatalf("expected exactly 1 post, got %d", got)
	}
	// Second poll must not post again.
	if err := e.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := posts.Load(); got != 1 {
		t.Fatalf("expected no second post, got %d", got)
	}
}
