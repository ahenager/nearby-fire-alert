// Package firms implements the NASA FIRMS active-fire provider. It fetches
// VIIRS/MODIS hotspot detections from the FIRMS Active Fire Data API (area/CSV
// endpoint), normalizing each detection into a provider-agnostic Event with a
// stable dedupe key (sensor + coords + acquisition date).
package firms

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ahenager/nearby-fire-alert/internal/httpx"
	"github.com/ahenager/nearby-fire-alert/internal/providerapi"
)

const (
	defaultAPIURL = "https://firms.modaps.eosdis.nasa.gov/api/area/csv"
	// defaultArea is the FIRMS bounding box in WestLon,SouthLat,EastLon,NorthLat
	// order (minLon,minLat,maxLon,maxLat). This is the ONLY order the FIRMS area
	// endpoint accepts; a different ordering yields HTTP 400.
	defaultArea          = "-125,24,-66,50"
	defaultInterval      = 5 * time.Minute
	defaultMinConfidence = 75.0
	defaultDays          = 1
)

// Provider implements providerapi.Provider for FIRMS hotspots.
type Provider struct {
	enabled  bool
	mapKey   string
	sensors  []string
	interval time.Duration
	minConf  float64
	apiURL   string
	area     string
	days     int
	client   *httpx.Client
}

// New returns a registered base provider. It is configured on Build().
func New() *Provider {
	return &Provider{}
}

// Name returns the provider name.
func (p *Provider) Name() string { return "firms" }

// Configure decodes the firms config block from cfg (file tier) and the
// resolved env+secret lookup (secret-file + env tiers).
func (p *Provider) Configure(cfg map[string]any, get func(key string) (string, bool)) error {
	c := providerapi.NewConf(cfg, get)
	p.enabled = c.Bool("FIRMS_ENABLED", "enabled", false)
	p.mapKey = c.String("FIRMS_MAP_KEY", "map_key", "")
	p.sensors = normalizeSensors(c.Slice("FIRMS_SENSORS", "sensors", []string{
		"VIIRS_SNPP", "VIIRS_NOAA20",
	}))
	p.interval = c.Duration("FIRMS_INTERVAL_SECONDS", "interval_seconds", defaultInterval)
	p.minConf = c.Float("FIRMS_MIN_CONFIDENCE", "min_confidence", defaultMinConfidence)
	p.apiURL = c.String("FIRMS_API_URL", "api_url", defaultAPIURL)
	p.area = c.String("FIRMS_AREA", "area", defaultArea)
	p.days = clampDays(c.Int("FIRMS_DAYS", "days", defaultDays))
	p.client = httpx.NewClient(30 * time.Second)
	return nil
}

// Enabled reports whether the provider should run.
func (p *Provider) Enabled() bool { return p.enabled }

// Interval returns the configured poll interval.
func (p *Provider) Interval() time.Duration { return p.interval }

// ConfidenceFloor returns the minimum hotspot confidence the core should alert on.
func (p *Provider) ConfidenceFloor() float64 { return p.minConf }

// Fetch returns normalized hotspot events observed since the last poll. It is
// resilient across days/sensors: a single failed day (e.g. an out-of-window
// `day` value) is skipped and logged as debug, not fatal to the whole poll.
func (p *Provider) Fetch(ctx context.Context, since time.Time) ([]providerapi.Event, error) {
	if p.mapKey == "" {
		return nil, fmt.Errorf("firms: MAP_KEY not configured (set FIRMS_MAP_KEY or FIRMS_MAP_KEY_FILE)")
	}
	var out []providerapi.Event
	var errs []string
	for _, sensor := range p.sensors {
		for d := 0; d < p.days; d++ {
			events, err := p.fetchSensorDay(ctx, sensor, d)
			if err != nil {
				errs = append(errs, err.Error())
				continue
			}
			for _, e := range events {
				if since.IsZero() || !e.DetectedAt.Before(since) {
					out = append(out, e)
				}
			}
		}
	}
	if len(out) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("firms: %s", strings.Join(errs, "; "))
	}
	return out, nil
}

func (p *Provider) fetchSensorDay(ctx context.Context, sensor string, day int) ([]providerapi.Event, error) {
	url := fmt.Sprintf("%s/%s/%s/%s/%d", strings.TrimRight(p.apiURL, "/"), p.mapKey, sensor, p.area, day)
	var status int
	var body []byte
	err := httpx.Retry(ctx, httpx.RetryOption{
		MaxAttempts: 3,
		InitialWait: time.Second,
		MaxWait:     10 * time.Second,
		ShouldRetry: func(code int) bool { return code == http.StatusTooManyRequests || code >= 500 },
	}, func() (bool, error) {
		st, b, e := p.client.Get(ctx, url, nil)
		status, body = st, b
		if e != nil {
			return false, e
		}
		if st == http.StatusTooManyRequests || st >= 500 {
			return false, fmt.Errorf("transient HTTP %d", st)
		}
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("firms: fetch %s: %w", sensor, err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("firms: %s returned HTTP %d", sensor, status)
	}
	events, err := parseCSV(body, sensor)
	if err != nil {
		return nil, fmt.Errorf("firms: parse %s: %w", sensor, err)
	}
	return events, nil
}

// sourceForSensor maps a FIRMS sensor value to the canonical source name used
// in Event.Source and config validation.
func sourceForSensor(sensor string) string {
	switch strings.ToUpper(sensor) {
	case "VIIRS_SNPP":
		return "viirs-snpp"
	case "VIIRS_NOAA20":
		return "viirs-noaa20"
	case "VIIRS_NOAA21":
		return "viirs-noaa21"
	case "MODIS":
		return "modis"
	default:
		return strings.ToLower(sensor)
	}
}

func normalizeSensors(s []string) []string {
	var out []string
	for _, v := range s {
		v = strings.ToUpper(strings.TrimSpace(v))
		if v == "" {
			continue
		}
		out = append(out, v)
	}
	return out
}

func clampDays(d int) int {
	if d < 1 {
		return 1
	}
	// Empirically the area endpoint accepts day 0-5 ("0" = current day); values
	// beyond that return HTTP 400 ("no data window"). Cap the safe range.
	if d > 5 {
		return 5
	}
	return d
}

// parseCSV parses a FIRMS active-fire CSV body into normalized events. It
// locates columns by header name so it is robust to field ordering and extra
// columns.
func parseCSV(body []byte, sensor string) ([]providerapi.Event, error) {
	r := csv.NewReader(strings.NewReader(string(body)))
	r.ReuseRecord = true
	r.TrimLeadingSpace = true

	header, err := r.Read()
	if err == io.EOF {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	idx := map[string]int{}
	for i, h := range header {
		idx[strings.TrimSpace(strings.ToLower(h))] = i
	}
	latIdx, ok1 := idx["latitude"]
	lonIdx, ok2 := idx["longitude"]
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("csv missing latitude/longitude columns: %v", header)
	}
	confIdx := idx["confidence"]
	frpIdx := idx["frp"]
	btIdx := idx["bright_ti4"]
	dateIdx := idx["acq_date"]
	timeIdx := idx["acq_time"]
	satIdx := idx["satellite"]

	source := sourceForSensor(sensor)
	var events []providerapi.Event
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		lat, latOK := parseFloatAt(rec, latIdx)
		lon, lonOK := parseFloatAt(rec, lonIdx)
		if !latOK || !lonOK {
			continue
		}
		conf := parseFloatOr(rec, confIdx, 0)
		frp := parseFloatOr(rec, frpIdx, 0)
		bt := parseFloatOr(rec, btIdx, 0)
		dateStr := at(rec, dateIdx)
		timeStr := at(rec, timeIdx)
		sat := at(rec, satIdx)
		if sat == "" {
			sat = sensor
		}
		detectedAt := buildDetectedAt(dateStr, timeStr)
		if detectedAt.IsZero() {
			continue
		}
		eventID := fmt.Sprintf("firms:%s:%.4f,%.4f:%s", sourceForSensor(sat), lat, lon, dateStr)
		events = append(events, providerapi.Event{
			EventID:         eventID,
			Kind:            "hotspot",
			Source:          source,
			DetectedAt:      detectedAt,
			Latitude:        lat,
			Longitude:       lon,
			Confidence:      conf,
			FRP:             frp,
			BrightnessTempK: bt,
		})
	}
	return events, nil
}

func at(rec []string, i int) string {
	if i < 0 || i >= len(rec) {
		return ""
	}
	return strings.TrimSpace(rec[i])
}

func parseFloatAt(rec []string, i int) (float64, bool) {
	return parseFloat(rec, i)
}

func parseFloatOr(rec []string, i int, def float64) float64 {
	v, ok := parseFloat(rec, i)
	if !ok {
		return def
	}
	return v
}

func parseFloat(rec []string, i int) (float64, bool) {
	s := at(rec, i)
	if s == "" {
		return 0, false
	}
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	if err != nil {
		return 0, false
	}
	return f, true
}

func buildDetectedAt(dateStr, timeStr string) time.Time {
	// dateStr: YYYY-MM-DD, timeStr: HHMM (UTC).
	if dateStr == "" {
		return time.Time{}
	}
	date, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return time.Time{}
	}
	var hh, mm int
	if timeStr != "" {
		fmt.Sscanf(timeStr, "%02d%02d", &hh, &mm)
	}
	return time.Date(date.Year(), date.Month(), date.Day(), hh, mm, 0, 0, time.UTC)
}
