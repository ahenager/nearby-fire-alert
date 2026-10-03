// Package providerapi defines the provider-agnostic normalized Event type and
// the Provider interface / Registry used to extend the service with new
// sources. Adding a source requires a new package implementing Provider plus a
// single Register call in main -- no changes to the core detection, geofence,
// dedup or OpenClaw logic.
package providerapi

import (
	"context"
	"fmt"
	"time"

	"github.com/ahenager/nearby-fire-alert/internal/geo"
)

// Event is a normalized, provider-agnostic fire event consumed by the core.
type Event struct {
	EventID         string    // stable, dedupe key
	Kind            string    // "hotspot" | "perimeter"
	Source          string    // provider name, e.g. "viirs-snpp"
	DetectedAt      time.Time // UTC
	Latitude        float64
	Longitude       float64
	DistanceMiles   float64 // computed by geofence at evaluation time
	Confidence      float64 // 0-100
	FRP             float64 // fire radiative power, 0 if n/a
	BrightnessTempK float64 // 0 if n/a
	IncidentName    string
	Perimeter       *geo.Polygon // optional normalized perimeter ring
}

// Provider is implemented by each fire source.
type Provider interface {
	// Name returns the unique provider name used in config and logs.
	Name() string
	// Configure decodes this provider's config block. get is the resolved
	// environment + secret-file lookup (four-tier loader).
	Configure(cfg map[string]any, get func(key string) (string, bool)) error
	// Fetch returns new/updated events observed since the last poll.
	Fetch(ctx context.Context, since time.Time) ([]Event, error)
	// Enabled indicates whether this provider should run.
	Enabled() bool
}

// Registry holds registered providers by name.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{providers: map[string]Provider{}}
}

// Register adds a provider. Duplicate names panic (programmer error).
func (r *Registry) Register(p Provider) {
	if p == nil {
		panic("providerapi: Register(nil)")
	}
	if _, ok := r.providers[p.Name()]; ok {
		panic("providerapi: duplicate provider: " + p.Name())
	}
	r.providers[p.Name()] = p
}

// Names returns all registered provider names.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.providers))
	for n := range r.providers {
		names = append(names, n)
	}
	return names
}

// Build constructs a registered provider by name, decoding cfg via the given
// lookup. Returns an error when the name is unknown. Providers are constructed
// once in main and driven by a schedule, so the registered instance is
// configured in place and returned.
func (r *Registry) Build(name string, cfg map[string]any, get func(key string) (string, bool)) (Provider, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q not registered", name)
	}
	if err := p.Configure(cfg, get); err != nil {
		return nil, fmt.Errorf("configure provider %q: %w", name, err)
	}
	return p, nil
}
