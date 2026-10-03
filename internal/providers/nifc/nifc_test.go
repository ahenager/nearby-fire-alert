package nifc

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ahenager/nearby-fire-alert/internal/httpx"
)

const sampleGeoJSON = `{
  "type": "FeatureCollection",
  "features": [
    {
      "type": "Feature",
      "properties": {"poly_IRWINID": "2023-CA-MTP-0123", "poly_IncidentName": "Test Fire", "attr_FireDiscoveryDateTime": "2023-06-01T00:00:00.000Z"},
      "geometry": {
        "type": "Polygon",
        "coordinates": [[[-97.0,35.0],[-96.0,35.0],[-96.0,36.0],[-97.0,36.0],[-97.0,35.0]]]
      }
    }
  ]
}`

func TestParseGeoJSON(t *testing.T) {
	events, err := parseGeoJSON([]byte(sampleGeoJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	e := events[0]
	if e.EventID != "nifc:2023-CA-MTP-0123" {
		t.Fatalf("bad id: %s", e.EventID)
	}
	if e.Kind != "perimeter" || e.Source != "nifc" {
		t.Fatalf("bad kind/source: %s/%s", e.Kind, e.Source)
	}
	if e.Confidence != 100 {
		t.Fatalf("expected confirmed confidence, got %v", e.Confidence)
	}
	if e.IncidentName != "Test Fire" {
		t.Fatalf("bad name: %s", e.IncidentName)
	}
	if e.Perimeter == nil || len(*e.Perimeter) < 4 {
		t.Fatal("expected a perimeter polygon")
	}
	if !e.DetectedAt.Equal(time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("bad detected_at: %v", e.DetectedAt)
	}
}

func TestConfigureAndFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, sampleGeoJSON)
	}))
	defer srv.Close()

	env := map[string]string{
		"NIFC_ENABLED":       "true",
		"NIFC_PERIMETER_URL": srv.URL,
	}
	get := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	p := New()
	if err := p.Configure(nil, get); err != nil {
		t.Fatal(err)
	}
	if !p.Enabled() {
		t.Fatal("should be enabled")
	}
	events, err := p.Fetch(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

func TestTrackHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := &Provider{url: srv.URL, client: httpx.NewClient(5 * time.Second)}
	if _, err := p.Fetch(context.Background(), time.Time{}); err == nil {
		t.Fatal("expected error on 401")
	}
}

func TestParseGeoJSONArcGISError(t *testing.T) {
	// ArcGIS returns HTTP 200 with an embedded error object (e.g. quota/token).
	_, err := parseGeoJSON([]byte(`{"error":{"code":499,"message":"Token Required","details":["..."]}}`))
	if err == nil {
		t.Fatal("expected error for ArcGIS error body")
	}
}
