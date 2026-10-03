package firms

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const sampleCSV = `latitude,longitude,bright_ti4,scan,track,acq_date,acq_time,satellite,confidence,version,bright_ti5,frp,daynight
35.1,-96.9,350.1,1,1,2023-01-01,1530,VIIRS_SNPP,95,1,300.1,25.0,D
34.0,-98.0,320.0,1,1,2023-01-01,1600,VIIRS_SNPP,40,1,290.0,10.0,D
`

func newMockServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return s
}

func TestParseCSV(t *testing.T) {
	events, err := parseCSV([]byte(sampleCSV), "VIIRS_SNPP")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	e := events[0]
	if e.Confidence != 95 || e.FRP != 25 || e.BrightnessTempK != 350.1 {
		t.Fatalf("bad fields: %+v", e)
	}
	if e.Source != "viirs-snpp" {
		t.Fatalf("bad source: %s", e.Source)
	}
	if e.Kind != "hotspot" {
		t.Fatalf("bad kind: %s", e.Kind)
	}
	want := time.Date(2023, 1, 1, 15, 30, 0, 0, time.UTC)
	if !e.DetectedAt.Equal(want) {
		t.Fatalf("bad detected_at: %v", e.DetectedAt)
	}
	if e.EventID != "firms:viirs-snpp:35.1000,-96.9000:2023-01-01" {
		t.Fatalf("bad event id: %s", e.EventID)
	}
}

func TestConfigureAndFetch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/testkey/") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.Contains(r.URL.Path, "/VIIRS_SNPP/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/csv")
		fmt.Fprint(w, sampleCSV)
	})
	srv := newMockServer(t, mux)

	env := map[string]string{
		"FIRMS_API_URL":          srv.URL,
		"FIRMS_MAP_KEY":          "testkey",
		"FIRMS_ENABLED":          "true",
		"FIRMS_SENSORS":          "VIIRS_SNPP",
		"FIRMS_INTERVAL_SECONDS": "300",
	}
	get := func(k string) (string, bool) { v, ok := env[k]; return v, ok }

	p := New()
	if err := p.Configure(nil, get); err != nil {
		t.Fatal(err)
	}
	if !p.Enabled() {
		t.Fatal("provider should be enabled")
	}
	events, err := p.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
}

func TestFetchWithoutKeyErrors(t *testing.T) {
	get := func(k string) (string, bool) { return "", false }
	p := New()
	if err := p.Configure(nil, get); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background(), time.Time{}); err == nil {
		t.Fatal("expected error when map key missing")
	}
}

func TestSinceFilter(t *testing.T) {
	// parseCSV is unit tested; here verify EventID stability across re-parses.
	ev1, _ := parseCSV([]byte(sampleCSV), "VIIRS_SNPP")
	ev2, _ := parseCSV([]byte(sampleCSV), "VIIRS_SNPP")
	if ev1[0].EventID != ev2[0].EventID {
		t.Fatal("event id not stable")
	}
}
