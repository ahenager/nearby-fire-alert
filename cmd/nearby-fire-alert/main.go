// Command nearby-fire-alert is a deterministic, containerized Go service that
// watches publicly available fire data, establishes when a fire is genuinely
// nearby, and notifies a human through an OpenClaw Gateway HTTP hook.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ahenager/nearby-fire-alert/internal/config"
	"github.com/ahenager/nearby-fire-alert/internal/detect"
	"github.com/ahenager/nearby-fire-alert/internal/logx"
	"github.com/ahenager/nearby-fire-alert/internal/openclaw"
	"github.com/ahenager/nearby-fire-alert/internal/providerapi"
	"github.com/ahenager/nearby-fire-alert/internal/providers/firms"
	"github.com/ahenager/nearby-fire-alert/internal/providers/ngfs"
	"github.com/ahenager/nearby-fire-alert/internal/providers/nifc"
	"github.com/ahenager/nearby-fire-alert/internal/store"
)

// Build metadata injected via ldflags (see Makefile).
var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	var (
		configPath string
		versionF   bool
		dryRun     bool
		once       bool
		migrate    bool
	)
	flag.StringVar(&configPath, "config", "", "path to a YAML/JSON/TOML config file")
	flag.BoolVar(&versionF, "version", false, "print version/build info and exit")
	flag.BoolVar(&dryRun, "dry-run", false, "print what would be posted without sending")
	flag.BoolVar(&once, "once", false, "run a single poll cycle and exit")
	flag.BoolVar(&migrate, "migrate", false, "run schema migrations and exit")
	flag.Parse()

	if versionF {
		fmt.Printf("nearby-fire-alert version=%s commit=%s built=%s\n", version, commit, buildTime)
		return
	}

	cfg, err := config.Load(config.Options{ConfigPath: configPath})
	if err != nil {
		fatal("config-load", err)
	}

	logger := logx.New(logx.ParseLevel(cfg.Runtime.LogLevel))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := openStore(cfg)
	if err != nil {
		fatal("store-open", err)
	}
	defer st.Close()

	if err := st.Migrate(ctx); err != nil {
		fatal("store-migrate", err)
	}
	if migrate {
		logger.Info("migrations applied", "driver", cfg.DB.Driver)
		return
	}

	providers, err := buildProviders(cfg)
	if err != nil {
		fatal("provider-build", err)
	}

	notifier := openclaw.New(cfg.OpenClaw, logger)
	engine := detect.New(detect.Options{
		Config:    cfg,
		Store:     st,
		Providers: providers,
		Notifier:  notifier,
		Logger:    logger,
		DryRun:    dryRun,
	})

	_ = st.RecordHealth(ctx, store.Health{
		RunID:  "startup",
		At:     time.Now().UTC(),
		Level:  "info",
		Detail: "service starting",
	})

	if once {
		logger.Info("once: running a single poll cycle", "dry_run", dryRun)
		if err := engine.RunOnce(ctx); err != nil {
			fatal("run-once", err)
		}
		return
	}

	logger.Info("service starting",
		"driver", cfg.DB.Driver,
		"home", fmt.Sprintf("(%.4f,%.4f)", cfg.Geofence.HomeLat, cfg.Geofence.HomeLon),
		"radius_miles", cfg.Geofence.RadiusMiles,
		"buffer_miles", cfg.Geofence.BufferMiles,
		"providers", countEnabled(providers),
		"dry_run", dryRun,
	)
	if err := engine.Run(ctx); err != nil {
		logger.Warn("engine stopped", "err", err)
	}
	logger.Info("service stopped")
}

func openStore(cfg *config.Config) (store.Store, error) {
	if cfg.DB.Driver == "postgres" {
		return store.New("postgres", cfg.DB.DSN)
	}
	return store.New("sqlite", cfg.DB.SQLitePath)
}

func buildProviders(cfg *config.Config) ([]providerapi.Provider, error) {
	get := config.ResolveEnvSecret

	// Register providers. Adding a source = new package + one Register line
	// here; no core changes required.
	reg := providerapi.NewRegistry()
	reg.Register(firms.New())
	reg.Register(nifc.New())
	reg.Register(ngfs.New())

	// Reject config blocks that reference unregistered providers.
	registered := map[string]bool{}
	for _, n := range reg.Names() {
		registered[n] = true
	}
	for name := range cfg.Sources {
		if !registered[name] {
			return nil, fmt.Errorf("config references unregistered provider %q", name)
		}
	}

	var providers []providerapi.Provider
	for _, name := range reg.Names() {
		p, err := reg.Build(name, cfg.Sources[name], get)
		if err != nil {
			return nil, err
		}
		providers = append(providers, p)
	}
	return providers, nil
}

func countEnabled(providers []providerapi.Provider) int {
	n := 0
	for _, p := range providers {
		if p != nil && p.Enabled() {
			n++
		}
	}
	return n
}

func fatal(step string, err error) {
	fmt.Fprintf(os.Stderr, "nearby-fire-alert[%s]: %v\n", step, err)
	os.Exit(1)
}
