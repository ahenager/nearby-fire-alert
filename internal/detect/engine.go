// Package detect implements the core detection, geofence, dedup and escalation
// engine. It consumes provider events, decides when a fire is genuinely nearby,
// and POSTs a single alert per real event (with sane escalation) via the
// OpenClaw client. All logic is deterministic; there is no LLM or randomness.
package detect

import (
	"context"
	"time"

	"github.com/ahenager/nearby-fire-alert/internal/config"
	"github.com/ahenager/nearby-fire-alert/internal/geo"
	"github.com/ahenager/nearby-fire-alert/internal/logx"
	"github.com/ahenager/nearby-fire-alert/internal/openclaw"
	"github.com/ahenager/nearby-fire-alert/internal/providerapi"
	"github.com/ahenager/nearby-fire-alert/internal/store"
)

// Options configures an Engine.
type Options struct {
	Config    *config.Config
	Store     store.Store
	Providers []providerapi.Provider
	Notifier  *openclaw.Client
	Logger    logx.Logger
	DryRun    bool
}

// Engine coordinates providers, the store, and the notifier.
type Engine struct {
	cfg       *config.Config
	store     store.Store
	providers []providerapi.Provider
	notifier  *openclaw.Client
	logger    logx.Logger
	dryRun    bool
	home      geo.Point
	alertR    float64
	bufferR   float64
	lastFetch map[string]time.Time
}

// New creates an Engine from options.
func New(opts Options) *Engine {
	cfg := opts.Config
	return &Engine{
		cfg:       cfg,
		store:     opts.Store,
		providers: opts.Providers,
		notifier:  opts.Notifier,
		logger:    opts.Logger,
		dryRun:    opts.DryRun,
		home:      geo.Point{Latitude: cfg.Geofence.HomeLat, Longitude: cfg.Geofence.HomeLon},
		alertR:    cfg.Geofence.RadiusMiles,
		bufferR:   cfg.Geofence.BufferMiles,
		lastFetch: map[string]time.Time{},
	}
}

// Run drives every enabled provider on its own schedule and maintains health
// + TTL cleanup. It returns when ctx is cancelled (graceful shutdown).
func (e *Engine) Run(ctx context.Context) error {
	if e.logger == nil {
		e.logger = logx.Discard()
	}
	// TTL cleanup + health sweep.
	go e.maintenanceLoop(ctx)
	// Provider loops.
	for _, p := range e.providers {
		if p == nil || !p.Enabled() {
			continue
		}
		go e.providerLoop(ctx, p)
	}
	<-ctx.Done()
	return ctx.Err()
}

// RunOnce polls every enabled provider a single time and returns. It is used by
// --once/--dry-run modes.
func (e *Engine) RunOnce(ctx context.Context) error {
	if e.logger == nil {
		e.logger = logx.Discard()
	}
	e.cleanup(ctx)
	for _, p := range e.providers {
		if p == nil || !p.Enabled() {
			continue
		}
		e.poll(ctx, p)
	}
	return nil
}

func (e *Engine) interval(p providerapi.Provider) time.Duration {
	if s, ok := p.(interface{ Interval() time.Duration }); ok {
		if d := s.Interval(); d > 0 {
			return d
		}
	}
	return 5 * time.Minute
}

func (e *Engine) providerLoop(ctx context.Context, p providerapi.Provider) {
	e.poll(ctx, p)
	ticker := time.NewTicker(e.interval(p))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.poll(ctx, p)
		}
	}
}

func (e *Engine) poll(ctx context.Context, p providerapi.Provider) {
	since := e.lastFetch[p.Name()]
	events, err := p.Fetch(ctx, since)
	if err != nil {
		e.logger.Error("provider fetch failed", "source", p.Name(), "err", err)
		return
	}
	e.lastFetch[p.Name()] = time.Now().UTC()
	for _, ev := range events {
		e.evaluate(ctx, p, ev)
	}
}

func (e *Engine) maintenanceLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.cleanup(ctx)
		}
	}
}

func (e *Engine) cleanup(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-time.Duration(e.cfg.Runtime.EventTTLHours) * time.Hour)
	if n, err := e.store.DeleteExpiredEvents(ctx, cutoff); err != nil {
		e.logger.Warn("ttl cleanup failed", "err", err)
	} else if n > 0 {
		e.logger.Info("expired events cleaned", "count", n)
	}
	_ = e.store.RecordHealth(ctx, store.Health{
		RunID:  "run",
		At:     time.Now().UTC(),
		Level:  "info",
		Detail: "maintenance sweep",
	})
}

// floorFor returns the confidence floor for a provider. Providers may expose a
// ConfidenceFloor method; otherwise no floor is applied (perimeters and other
// sources that are confirmed by nature are not confidence-filtered).
func (e *Engine) floorFor(p providerapi.Provider) float64 {
	if f, ok := p.(interface{ ConfidenceFloor() float64 }); ok {
		return f.ConfidenceFloor()
	}
	return 0
}

func (e *Engine) evaluate(ctx context.Context, p providerapi.Provider, ev providerapi.Event) {
	pos := geo.Point{Latitude: ev.Latitude, Longitude: ev.Longitude}
	d := geo.HaversineMiles(e.home, pos)
	ev.DistanceMiles = d
	zone := geo.Classify(e.home, pos, e.alertR, e.bufferR)
	minConf := e.floorFor(p)

	eligible := false
	switch ev.Kind {
	case "hotspot":
		if zone == geo.ZoneWatch {
			e.record(ctx, ev, "watch", "in buffer but outside alert radius")
			e.logger.Debug("watched (no alert)", "event_id", ev.EventID, "distance_miles", d, "source", ev.Source)
			return
		}
		if zone != geo.ZoneAlert {
			e.record(ctx, ev, "skip", "outside alert radius")
			e.logger.Debug("skipped (outside radius)", "event_id", ev.EventID, "distance_miles", d, "source", ev.Source)
			return
		}
		if ev.Confidence < minConf {
			e.record(ctx, ev, "skip", "below min confidence")
			e.logger.Debug("skipped (low confidence)", "event_id", ev.EventID, "confidence", ev.Confidence, "min", minConf)
			return
		}
		eligible = true
	case "perimeter":
		intersect := ev.Perimeter != nil && ev.Perimeter.IntersectsCircle(e.home, e.bufferR)
		if !intersect && zone != geo.ZoneAlert {
			e.record(ctx, ev, "skip", "perimeter outside buffer")
			e.logger.Debug("perimeter skipped (outside buffer)", "event_id", ev.EventID, "incident", ev.IncidentName)
			return
		}
		if zone == geo.ZoneAlert {
			ev.DistanceMiles = d
		}
		eligible = true
	default:
		e.logger.Debug("unknown event kind ignored", "event_id", ev.EventID, "kind", ev.Kind)
		return
	}

	if !eligible {
		return
	}

	known, ok, err := e.store.GetKnownEvent(ctx, ev.EventID)
	if err != nil {
		e.logger.Error("store lookup failed", "event_id", ev.EventID, "err", err)
		return
	}
	if ok && known.Delivered {
		if e.maybeEscalate(ev, known) {
			e.deliver(ctx, ev, "escalation")
		} else {
			e.refreshKnown(ctx, ev)
			e.record(ctx, ev, "known", "already delivered; no material change")
			e.logger.Debug("no re-alert (dedup)", "event_id", ev.EventID)
		}
		return
	}
	// New event, or a pending (not-yet-delivered) known event -> deliver once.
	e.deliver(ctx, ev, "new")
}

func (e *Engine) deliver(ctx context.Context, ev providerapi.Event, reason string) {
	now := time.Now().UTC()
	// Persist the event so dedupe works even if delivery is retried.
	if err := e.store.UpsertKnownEvent(ctx, store.KnownEvent{
		EventID:        ev.EventID,
		Kind:           ev.Kind,
		Source:         ev.Source,
		FirstSeen:      now,
		LastSeen:       now,
		LastConfidence: ev.Confidence,
		LastFRP:        ev.FRP,
	}); err != nil {
		e.logger.Error("store upsert failed", "event_id", ev.EventID, "err", err)
		return
	}

	summary := openclaw.Summary(ev)
	payload := openclaw.BuildPayload(ev, summary, e.cfg.OpenClaw)

	if e.dryRun {
		e.logger.Info("dry-run: would post", "event_id", ev.EventID, "kind", ev.Kind, "source", ev.Source, "reason", reason, "payload", payload)
		e.record(ctx, ev, "alert", "dry-run:"+reason)
		return
	}

	res, err := e.notifier.Post(ctx, payload)
	if err != nil {
		e.logger.Error("delivery failed", "event_id", ev.EventID, "err", err, "reason", reason)
		e.record(ctx, ev, "alert", "delivery_failed:"+reason)
		return
	}
	if res.Delivered {
		_ = e.store.MarkDelivered(ctx, ev.EventID, now)
		e.record(ctx, ev, "alert", reason)
		e.logger.Info("delivered alert",
			"event_id", ev.EventID, "run_id", res.RunID, "status", res.StatusCode, "reason", reason,
			"source", ev.Source, "kind", ev.Kind, "distance_miles", ev.DistanceMiles)
	} else {
		e.logger.Warn("hook returned 2xx without admission", "event_id", ev.EventID, "status", res.StatusCode)
		e.record(ctx, ev, "alert", "no_admission:"+reason)
	}
}

// maybeEscalate reports whether the event represents a material change that
// warrants a re-alert: a confidence or FRP rise by >= factor, or persistence
// beyond the configured threshold (respecting a cooldown since last delivery).
func (e *Engine) maybeEscalate(ev providerapi.Event, known store.KnownEvent) bool {
	factor := e.cfg.Runtime.EscalationFactor
	if factor <= 0 {
		factor = 0.20
	}
	confRise := known.LastConfidence > 0 && ev.Confidence >= known.LastConfidence*(1+factor)
	frpRise := known.LastFRP > 0 && ev.FRP >= known.LastFRP*(1+factor)

	if confRise || frpRise {
		return true
	}
	if e.cfg.Runtime.EscalationPersistHours > 0 && !known.FirstSeen.IsZero() {
		age := time.Since(known.FirstSeen)
		if age >= time.Duration(e.cfg.Runtime.EscalationPersistHours)*time.Hour {
			cooldown := time.Duration(e.cfg.Runtime.EscalationCooldownMin) * time.Minute
			if known.DeliveredAt == nil || cooldown <= 0 || time.Since(*known.DeliveredAt) >= cooldown {
				return true
			}
		}
	}
	return false
}

// refreshKnown updates last_seen/confidence/frp without touching delivered state.
func (e *Engine) refreshKnown(ctx context.Context, ev providerapi.Event) {
	if err := e.store.UpsertKnownEvent(ctx, store.KnownEvent{
		EventID:        ev.EventID,
		Kind:           ev.Kind,
		Source:         ev.Source,
		LastSeen:       time.Now().UTC(),
		LastConfidence: ev.Confidence,
		LastFRP:        ev.FRP,
	}); err != nil {
		e.logger.Warn("refresh known failed", "event_id", ev.EventID, "err", err)
	}
}

func (e *Engine) record(ctx context.Context, ev providerapi.Event, decision, reason string) {
	_ = e.store.RecordDetection(ctx, store.Detection{
		At:        time.Now().UTC(),
		Source:    ev.Source,
		EventID:   ev.EventID,
		Decision:  decision,
		Reason:    reason,
		Latitude:  ev.Latitude,
		Longitude: ev.Longitude,
	})
}
