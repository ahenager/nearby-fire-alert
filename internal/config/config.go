// Package config implements the four-tier configuration loader:
//
//  1. Built-in code defaults
//  2. Config file (YAML/JSON/TOML) from -config / CONFIG_FILE
//  3. Environment variables
//  4. Secret files (<NAME>_FILE) -- highest precedence
//
// Every field is resolvable through any tier. Secret files win over env and
// over the config file, because mounted secrets are the most authoritative
// source in Docker/Kubernetes. Type coercion (ints, floats, bools, slices,
// durations) is handled once in the loader.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// Config is the fully-resolved service configuration.
type Config struct {
	Geofence GeofenceConfig            `yaml:"geofence" toml:"geofence" json:"geofence"`
	Sources  map[string]map[string]any `yaml:"sources" toml:"sources" json:"sources"`
	OpenClaw OpenClawConfig            `yaml:"openclaw" toml:"openclaw" json:"openclaw"`
	DB       DBConfig                  `yaml:"db" toml:"db" json:"db"`
	Runtime  RuntimeConfig             `yaml:"runtime" toml:"runtime" json:"runtime"`
}

// GeofenceConfig holds home location and radii (miles).
type GeofenceConfig struct {
	HomeLat     float64 `yaml:"home_lat" toml:"home_lat" json:"home_lat"`
	HomeLon     float64 `yaml:"home_lon" toml:"home_lon" json:"home_lon"`
	RadiusMiles float64 `yaml:"radius_miles" toml:"radius_miles" json:"radius_miles"`
	BufferMiles float64 `yaml:"buffer_miles" toml:"buffer_miles" json:"buffer_miles"`
}

// OpenClawConfig holds gateway hook and delivery routing settings.
type OpenClawConfig struct {
	HookURL        string `yaml:"hook_url" toml:"hook_url" json:"hook_url"`
	HookToken      string `yaml:"hook_token" toml:"hook_token" json:"hook_token"`
	AgentID        string `yaml:"agent_id" toml:"agent_id" json:"agent_id"`
	Channel        string `yaml:"channel" toml:"channel" json:"channel"`
	To             string `yaml:"to" toml:"to" json:"to"`
	AccountID      string `yaml:"account_id" toml:"account_id" json:"account_id"`
	TimeoutSeconds int    `yaml:"timeout_seconds" toml:"timeout_seconds" json:"timeout_seconds"`
}

// DBConfig selects the database driver and connection details.
type DBConfig struct {
	Driver     string `yaml:"driver" toml:"driver" json:"driver"` // sqlite | postgres
	DSN        string `yaml:"dsn" toml:"dsn" json:"dsn"`          // postgres URL; ignored when driver=sqlite
	SQLitePath string `yaml:"sqlite_path" toml:"sqlite_path" json:"sqlite_path"`
}

// RuntimeConfig holds runtime behaviour.
type RuntimeConfig struct {
	EventTTLHours          int     `yaml:"event_ttl_hours" toml:"event_ttl_hours" json:"event_ttl_hours"`
	LogLevel               string  `yaml:"log_level" toml:"log_level" json:"log_level"`
	EscalationFactor       float64 `yaml:"escalation_factor" toml:"escalation_factor" json:"escalation_factor"`
	EscalationPersistHours int     `yaml:"escalation_persist_hours" toml:"escalation_persist_hours" json:"escalation_persist_hours"`
	EscalationCooldownMin  int     `yaml:"escalation_cooldown_minutes" toml:"escalation_cooldown_minutes" json:"escalation_cooldown_minutes"`
}

// Options selects how the loader resolves its inputs.
type Options struct {
	// ConfigPath is taken from -config or CONFIG_FILE.
	ConfigPath string
	// Getenv is the environment lookup; defaults to os.LookupEnv when nil.
	Getenv func(string) (string, bool)
	// ReadFile is the secret-file reader; defaults to os.ReadFile when nil.
	ReadFile func(string) ([]byte, error)
}

// defaults returns the lowest-precedence code defaults.
func defaults() *Config {
	return &Config{
		Geofence: GeofenceConfig{
			HomeLat:     39.5,
			HomeLon:     -98.35,
			RadiusMiles: 5,
			BufferMiles: 25,
		},
		Sources: map[string]map[string]any{},
		OpenClaw: OpenClawConfig{
			HookURL:        "",
			HookToken:      "",
			AgentID:        "main",
			Channel:        "signal",
			To:             "",
			AccountID:      "",
			TimeoutSeconds: 15,
		},
		DB: DBConfig{
			Driver:     "sqlite",
			DSN:        "",
			SQLitePath: "./state.db",
		},
		Runtime: RuntimeConfig{
			EventTTLHours:          24,
			LogLevel:               "info",
			EscalationFactor:       0.20,
			EscalationPersistHours: 24,
			EscalationCooldownMin:  60,
		},
	}
}

type loader struct {
	getenv   func(string) (string, bool)
	readFile func(string) ([]byte, error)
}

func (l *loader) env(name string) (string, bool) {
	if l.getenv == nil {
		return os.LookupEnv(name)
	}
	return l.getenv(name)
}

// secret returns the value from <NAME>_FILE if set and readable. Trims
// surrounding whitespace (including a trailing newline).
func (l *loader) secret(name string) (string, bool) {
	fp, ok := l.env(name + "_FILE")
	if !ok || strings.TrimSpace(fp) == "" {
		return "", false
	}
	var data []byte
	var err error
	if l.readFile != nil {
		data, err = l.readFile(fp)
	} else {
		data, err = os.ReadFile(fp)
	}
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}

// resolve attempts, in order, secret file then env. Returns ok=false if both
// are absent (caller falls back to config-file/default value).
func (l *loader) resolve(name string) (string, bool) {
	if v, ok := l.secret(name); ok {
		return v, true
	}
	return l.env(name)
}

// EnvLookup is a resolved env+secret lookup suitable for providers. Secret
// files win over env. It is passed to Provider.Configure as `get`.
func (l *loader) EnvLookup(name string) (string, bool) {
	return l.resolve(name)
}

// ResolveEnvSecret resolves a value using the default OS env + secret-file
// lookup (secret file wins over env). It is the provider-facing `get` callback.
func ResolveEnvSecret(name string) (string, bool) {
	l := &loader{getenv: os.LookupEnv, readFile: os.ReadFile}
	return l.resolve(name)
}

// Load resolves the configuration across all four tiers.
func Load(opts Options) (*Config, error) {
	if opts.Getenv == nil {
		opts.Getenv = os.LookupEnv
	}
	if opts.ReadFile == nil {
		opts.ReadFile = os.ReadFile
	}
	l := &loader{getenv: opts.Getenv, readFile: opts.ReadFile}

	cfg := defaults()

	path := opts.ConfigPath
	if path == "" {
		if p, ok := l.env("CONFIG_FILE"); ok {
			path = strings.TrimSpace(p)
		}
	}
	if path != "" {
		if err := parseFile(path, cfg); err != nil {
			return nil, fmt.Errorf("parse config file %q: %w", path, err)
		}
	}

	// Env + secret overlays for root config (4th tier secret wins).
	overlayRoot(l, cfg)

	return cfg, nil
}

func overlayRoot(l *loader, cfg *Config) {
	cfg.Geofence.HomeLat = resolveFloat(l, "GEOFENCE_HOME_LAT", cfg.Geofence.HomeLat)
	cfg.Geofence.HomeLon = resolveFloat(l, "GEOFENCE_HOME_LON", cfg.Geofence.HomeLon)
	cfg.Geofence.RadiusMiles = resolveFloat(l, "GEOFENCE_RADIUS_MILES", cfg.Geofence.RadiusMiles)
	cfg.Geofence.BufferMiles = resolveFloat(l, "GEOFENCE_BUFFER_MILES", cfg.Geofence.BufferMiles)

	cfg.OpenClaw.HookURL = resolveString(l, "OPENCLAW_HOOK_URL", cfg.OpenClaw.HookURL)
	cfg.OpenClaw.HookToken = resolveString(l, "OPENCLAW_HOOK_TOKEN", cfg.OpenClaw.HookToken)
	cfg.OpenClaw.AgentID = resolveString(l, "OPENCLAW_AGENT_ID", cfg.OpenClaw.AgentID)
	cfg.OpenClaw.Channel = resolveString(l, "OPENCLAW_CHANNEL", cfg.OpenClaw.Channel)
	cfg.OpenClaw.To = resolveString(l, "OPENCLAW_TO", cfg.OpenClaw.To)
	cfg.OpenClaw.AccountID = resolveString(l, "OPENCLAW_ACCOUNT_ID", cfg.OpenClaw.AccountID)
	cfg.OpenClaw.TimeoutSeconds = resolveInt(l, "OPENCLAW_TIMEOUT_SECONDS", cfg.OpenClaw.TimeoutSeconds)

	cfg.DB.Driver = resolveString(l, "DB_DRIVER", cfg.DB.Driver)
	cfg.DB.DSN = resolveString(l, "DB_DSN", cfg.DB.DSN)
	cfg.DB.SQLitePath = resolveString(l, "DB_SQLITE_PATH", cfg.DB.SQLitePath)

	cfg.Runtime.EventTTLHours = resolveInt(l, "RUNTIME_EVENT_TTL_HOURS", cfg.Runtime.EventTTLHours)
	cfg.Runtime.LogLevel = resolveString(l, "RUNTIME_LOG_LEVEL", cfg.Runtime.LogLevel)
	cfg.Runtime.EscalationFactor = resolveFloat(l, "RUNTIME_ESCALATION_FACTOR", cfg.Runtime.EscalationFactor)
	cfg.Runtime.EscalationPersistHours = resolveInt(l, "RUNTIME_ESCALATION_PERSIST_HOURS", cfg.Runtime.EscalationPersistHours)
	cfg.Runtime.EscalationCooldownMin = resolveInt(l, "RUNTIME_ESCALATION_COOLDOWN_MINUTES", cfg.Runtime.EscalationCooldownMin)
}

func resolveString(l *loader, name, base string) string {
	if v, ok := l.resolve(name); ok {
		return v
	}
	return base
}

func resolveFloat(l *loader, name string, base float64) float64 {
	if v, ok := l.resolve(name); ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f
		}
	}
	return base
}

func resolveInt(l *loader, name string, base int) int {
	if v, ok := l.resolve(name); ok {
		if i, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return i
		}
	}
	return base
}

func parseFile(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	switch lowerExt(path) {
	case ".json":
		return json.Unmarshal(data, cfg)
	case ".toml":
		_, err := toml.Decode(string(data), cfg)
		return err
	case ".yaml", ".yml":
		return yaml.Unmarshal(data, cfg)
	default:
		// Unknown extension: try YAML first, then JSON, then TOML.
		if err := yaml.Unmarshal(data, cfg); err == nil {
			return nil
		}
		if err := json.Unmarshal(data, cfg); err == nil {
			return nil
		}
		if _, err := toml.Decode(string(data), cfg); err == nil {
			return nil
		}
		return errors.New("unsupported config format (expected YAML/JSON/TOML)")
	}
}

func lowerExt(path string) string {
	idx := strings.LastIndex(path, ".")
	if idx < 0 {
		return ""
	}
	return strings.ToLower(path[idx:])
}

// Coercions for provider config blocks -------------------------------------

// ToFloat coerces a value (float64, int, string) to float64.
func ToFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

// ToInt coerces a value to int.
func ToInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(x))
		return i, err == nil
	default:
		return 0, false
	}
}

// ToBool coerces a value to bool.
func ToBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(x))
		return b, err == nil
	default:
		return false, false
	}
}

// ToDuration coerces a value (seconds int/float, or a duration string) to a
// time.Duration.
func ToDuration(v any) (time.Duration, bool) {
	switch x := v.(type) {
	case float64:
		return time.Duration(x * float64(time.Second)), true
	case int:
		return time.Duration(x) * time.Second, true
	case int64:
		return time.Duration(x) * time.Second, true
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0, true
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, false
		}
		return d, true
	default:
		return 0, false
	}
}

// ToSlice splits a comma-separated string or []any (from YAML) into []string.
func ToSlice(v any) ([]string, bool) {
	switch x := v.(type) {
	case string:
		if strings.TrimSpace(x) == "" {
			return nil, true
		}
		parts := strings.Split(x, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out, true
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out, true
	default:
		return nil, false
	}
}
