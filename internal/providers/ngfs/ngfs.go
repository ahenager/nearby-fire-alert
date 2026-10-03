// Package ngfs provides a registered-but-disabled stub for the Next Generation
// Fire System (NGFS) source. It is intentionally a no-op that documents the
// intended AWS S3 / GOES event-driven consumer so a future implementation can
// be dropped in with minimal changes.
//
// Intended design (for a future implementation):
//   - Subscribe to AWS S3 bucket notifications for GOES-R active-fire objects
//     (e.g. s3://<bucket>/goes-fires/...).
//   - On event, download the object, parse the netCDF/GeoTIFF or JSON fire
//     product, and emit providerapi.Event entries of kind "hotspot".
//   - Apply the same false-positive and geofence logic as FIRMS; NGFS provides
//     higher spatiotemporal resolution than VIIRS.
//
// To enable this source later, set sources.ngfs.enabled=true and implement
// Fetch. No changes are required to the core detection/geofence/dedup/store/
// OpenClaw logic.
package ngfs

import (
	"context"
	"time"

	"github.com/ahenager/nearby-fire-alert/internal/providerapi"
)

const defaultInterval = 5 * time.Minute

// Provider implements providerapi.Provider as a disabled no-op stub.
type Provider struct {
	enabled  bool
	interval time.Duration
}

// New returns a registered base NGFS stub provider.
func New() *Provider { return &Provider{} }

// Name returns the provider name.
func (p *Provider) Name() string { return "ngfs" }

// Configure decodes the ngfs config block (enabled, interval_seconds).
func (p *Provider) Configure(cfg map[string]any, get func(key string) (string, bool)) error {
	c := providerapi.NewConf(cfg, get)
	p.enabled = c.Bool("NGFS_ENABLED", "enabled", false)
	p.interval = c.Duration("NGFS_INTERVAL_SECONDS", "interval_seconds", defaultInterval)
	return nil
}

// Enabled reports whether the provider should run. NGFS is disabled by default.
func (p *Provider) Enabled() bool { return p.enabled }

// Interval returns the configured poll interval.
func (p *Provider) Interval() time.Duration { return p.interval }

// Fetch returns no events; the stub documents the intended consumer only.
func (p *Provider) Fetch(ctx context.Context, since time.Time) ([]providerapi.Event, error) {
	return nil, nil
}
