// Package nifc implements the NIFC incident-perimeter provider. It fetches
// active incident perimeters as GeoJSON (feature collection) from a configurable
// endpoint, normalizing each incident polygon into a provider-agnostic Event of
// kind "perimeter". Perimeters are treated as high-confidence confirmations.
package nifc

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ahenager/nearby-fire-alert/internal/geo"
	"github.com/ahenager/nearby-fire-alert/internal/httpx"
	"github.com/ahenager/nearby-fire-alert/internal/providerapi"
)

const (
	// WFIGS Current Interagency Fire Perimeters (NIFC), the public GeoJSON
	// feature service. outSR=4326 yields WGS84 lon/lat coordinates.
	defaultPerimeterURL = "https://services3.arcgis.com/T4QMspbfLg3qTGWY/arcgis/rest/services/WFIGS_Interagency_Perimeters_Current/FeatureServer/0/query?where=1%3D1&outFields=*&f=geojson&outSR=4326"
	defaultInterval     = 15 * time.Minute
)

// Provider implements providerapi.Provider for NIFC perimeters.
type Provider struct {
	enabled  bool
	interval time.Duration
	url      string
	client   *httpx.Client
}

// New returns a registered base NIFC provider.
func New() *Provider { return &Provider{} }

// Name returns the provider name.
func (p *Provider) Name() string { return "nifc" }

// Configure decodes the nifc config block.
func (p *Provider) Configure(cfg map[string]any, get func(key string) (string, bool)) error {
	c := providerapi.NewConf(cfg, get)
	p.enabled = c.Bool("NIFC_ENABLED", "enabled", true)
	p.interval = c.Duration("NIFC_INTERVAL_SECONDS", "interval_seconds", defaultInterval)
	p.url = c.String("NIFC_PERIMETER_URL", "perimeter_url", defaultPerimeterURL)
	p.client = httpx.NewClient(30 * time.Second)
	return nil
}

// Enabled reports whether the provider should run.
func (p *Provider) Enabled() bool { return p.enabled }

// Interval returns the configured poll interval.
func (p *Provider) Interval() time.Duration { return p.interval }

// Fetch returns perimeter events observed since the last poll.
func (p *Provider) Fetch(ctx context.Context, since time.Time) ([]providerapi.Event, error) {
	var status int
	var body []byte
	err := httpx.Retry(ctx, httpx.RetryOption{
		MaxAttempts: 3,
		InitialWait: time.Second,
		MaxWait:     10 * time.Second,
		ShouldRetry: func(code int) bool { return code == http.StatusTooManyRequests || code >= 500 },
	}, func() (bool, error) {
		st, b, e := p.client.Get(ctx, p.url, map[string]string{
			"Accept": "application/geo+json, application/json",
		})
		status, body = st, b
		if e != nil {
			return false, e
		}
		if st == http.StatusTooManyRequests || st >= 500 {
			return false, fmt.Errorf("nifc: transient HTTP %d", st)
		}
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("nifc: fetch: %w", err)
	}
	if status == http.StatusForbidden || status == http.StatusUnauthorized {
		return nil, fmt.Errorf("nifc: HTTP %d (unauthorized)", status)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("nifc: HTTP %d", status)
	}
	events, err := parseGeoJSON(body)
	if err != nil {
		return nil, fmt.Errorf("nifc: parse: %w", err)
	}
	// NIFC perimeters represent ongoing incidents; since filters by detected_at
	// if the source provides one, otherwise all perimeters are returned.
	if since.IsZero() {
		return events, nil
	}
	var out []providerapi.Event
	for _, e := range events {
		if e.DetectedAt.IsZero() || !e.DetectedAt.Before(since) {
			out = append(out, e)
		}
	}
	return out, nil
}

type geoJSON struct {
	Type     string       `json:"type"`
	Features []feature    `json:"features"`
	Error    *arcgisError `json:"error"`
}

type arcgisError struct {
	Code    int      `json:"code"`
	Message string   `json:"message"`
	Details []string `json:"details"`
}

type feature struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
	Geometry   geometry       `json:"geometry"`
}

type geometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

func parseGeoJSON(body []byte) ([]providerapi.Event, error) {
	var fc geoJSON
	if err := json.Unmarshal(body, &fc); err != nil {
		return nil, err
	}
	if fc.Error != nil {
		return nil, fmt.Errorf("arcgis error %d: %s", fc.Error.Code, fc.Error.Message)
	}
	var events []providerapi.Event
	for _, f := range fc.Features {
		pg, err := polygonFromGeometry(f.Geometry)
		if err != nil {
			// Skip malformed or unsupported geometries rather than failing the batch.
			continue
		}
		if len(pg) < 3 {
			continue
		}
		id := incidentID(f.Properties)
		if id == "" {
			continue
		}
		name := pick(f.Properties, "incidentname", "name")
		rep := pg.RepresentativePoint()
		ev := providerapi.Event{
			EventID:      id,
			Kind:         "perimeter",
			Source:       "nifc",
			DetectedAt:   detectedAtFromProps(f.Properties),
			Latitude:     rep.Latitude,
			Longitude:    rep.Longitude,
			Confidence:   100, // treated as confirmed
			FRP:          0,
			IncidentName: name,
		}
		ev.Perimeter = &pg
		events = append(events, ev)
	}
	return events, nil
}

// polygonFromGeometry extracts the outer ring(s) of a Polygon/MultiPolygon as a
// geo.Polygon (closed). Coordinates in GeoJSON are [lon, lat].
func polygonFromGeometry(g geometry) (geo.Polygon, error) {
	switch g.Type {
	case "Polygon":
		var rings [][][]float64
		if err := json.Unmarshal(g.Coordinates, &rings); err != nil {
			return nil, err
		}
		if len(rings) == 0 {
			return nil, fmt.Errorf("empty ring")
		}
		return ringToPolygon(rings[0]), nil
	case "MultiPolygon":
		var polys [][][][]float64
		if err := json.Unmarshal(g.Coordinates, &polys); err != nil {
			return nil, err
		}
		if len(polys) == 0 {
			return nil, fmt.Errorf("empty multipolygon")
		}
		// Use the largest polygon's outer ring as the representative boundary.
		best := geo.Polygon{}
		bestArea := -1.0
		for _, poly := range polys {
			if len(poly) == 0 {
				continue
			}
			pg := ringToPolygon(poly[0])
			a := polygonArea(pg)
			if a > bestArea {
				bestArea = a
				best = pg
			}
		}
		return best, nil
	default:
		return nil, fmt.Errorf("unsupported geometry type %q", g.Type)
	}
}

func ringToPolygon(ring [][]float64) geo.Polygon {
	pg := make(geo.Polygon, 0, len(ring))
	for _, pos := range ring {
		if len(pos) < 2 {
			continue
		}
		pg = append(pg, geo.Point{Latitude: pos[1], Longitude: pos[0]})
	}
	return pg
}

func polygonArea(pg geo.Polygon) float64 {
	n := len(pg)
	if n < 3 {
		return 0
	}
	area := 0.0
	for i := 0; i < n; i++ {
		a := pg[i]
		b := pg[(i+1)%n]
		area += a.Longitude*b.Latitude - b.Longitude*a.Latitude
	}
	return math.Abs(area) / 2
}

func incidentID(props map[string]any) string {
	// The WFIGS/NIFC feed prefixes fields with poly_/attr_; match by substring
	// so the same parser works across feed schema variants.
	id := pick(props, "irwinid", "uniquefireidentifier", "forid", "globalid", "objectid")
	if id == "" {
		return ""
	}
	return "nifc:" + id
}

func detectedAtFromProps(props map[string]any) time.Time {
	s := pick(props, "firediscoverydatetime", "polygondatetime", "createdate", "createdon", "discoverydate")
	if s == "" {
		return time.Time{}
	}
	return parseTimeValue(s)
}

// parseTimeValue accepts common ArcGIS timestamp forms: RFC3339 (with or
// without fractional seconds/millis), "2006-01-02", and epoch milliseconds.
func parseTimeValue(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z0700", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	// Epoch milliseconds (output from some ArcGIS fields).
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1e12 { // ms
			return time.UnixMilli(n).UTC()
		}
		if n > 1e9 { // seconds
			return time.Unix(n, 0).UTC()
		}
	}
	return time.Time{}
}

// pick returns the first non-empty value whose (lowercased) property key
// contains any of the given needles. This makes parsing robust to the offset
// prefixes (poly_, attr_) used by NIFC/WFIGS feeds.
func pick(props map[string]any, needles ...string) string {
	for k, v := range props {
		lk := strings.ToLower(k)
		for _, n := range needles {
			if strings.Contains(lk, strings.ToLower(n)) {
				if s := valueString(v); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func valueString(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(x, 10)
	case json.Number:
		return x.String()
	default:
		return fmt.Sprintf("%v", x)
	}
}
