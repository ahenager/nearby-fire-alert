package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultsOnly(t *testing.T) {
	cfg, err := Load(Options{Getenv: func(string) (string, bool) { return "", false }})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Geofence.RadiusMiles != 5 || cfg.Geofence.BufferMiles != 25 {
		t.Fatalf("bad defaults: %+v", cfg.Geofence)
	}
	if cfg.DB.Driver != "sqlite" || cfg.Runtime.LogLevel != "info" {
		t.Fatalf("bad defaults: %+v", cfg)
	}
	if cfg.OpenClaw.TimeoutSeconds != 15 {
		t.Fatalf("bad timeout: %d", cfg.OpenClaw.TimeoutSeconds)
	}
}

func TestFileOverridesDefaults(t *testing.T) {
	path := writeTemp(t, "config.yaml", `
geofence:
  home_lat: 40.0
  radius_miles: 10
runtime:
  log_level: debug
`)
	cfg, err := Load(Options{ConfigPath: path, Getenv: func(string) (string, bool) { return "", false }})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Geofence.HomeLat != 40.0 || cfg.Geofence.RadiusMiles != 10 {
		t.Fatalf("file not applied: %+v", cfg.Geofence)
	}
	// Buffer should keep default (not in file).
	if cfg.Geofence.BufferMiles != 25 {
		t.Fatalf("default buffer clobbered: %v", cfg.Geofence.BufferMiles)
	}
	if cfg.Runtime.LogLevel != "debug" {
		t.Fatalf("log level not applied: %v", cfg.Runtime)
	}
}

func TestJSONAndTOMLParsing(t *testing.T) {
	jpath := writeTemp(t, "config.json", `{"geofence":{"radius_miles":7},"db":{"driver":"postgres"}}`)
	cfg, err := Load(Options{ConfigPath: jpath, Getenv: func(string) (string, bool) { return "", false }})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Geofence.RadiusMiles != 7 || cfg.DB.Driver != "postgres" {
		t.Fatalf("json parse failed: %+v", cfg)
	}

	tpath := writeTemp(t, "config.toml", "[geofence]\nradius_miles = 9\n")
	cfg2, err := Load(Options{ConfigPath: tpath, Getenv: func(string) (string, bool) { return "", false }})
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Geofence.RadiusMiles != 9 {
		t.Fatalf("toml parse failed: %+v", cfg2)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	path := writeTemp(t, "config.yaml", "geofence:\n  radius_miles: 3\n")
	env := map[string]string{
		"GEOFENCE_RADIUS_MILES": "20",
		"RUNTIME_LOG_LEVEL":     "warn",
	}
	getenv := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	cfg, err := Load(Options{ConfigPath: path, Getenv: getenv})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Geofence.RadiusMiles != 20 {
		t.Fatalf("env should override file: %v", cfg.Geofence.RadiusMiles)
	}
	if cfg.Runtime.LogLevel != "warn" {
		t.Fatalf("env log level: %v", cfg.Runtime.LogLevel)
	}
}

func TestSecretFileWinsOverEnv(t *testing.T) {
	secretPath := writeTemp(t, "token.txt", "  secret-value\n  ")
	env := map[string]string{
		"OPENCLAW_HOOK_TOKEN_FILE": secretPath,
		"OPENCLAW_HOOK_TOKEN":      "ill-be-overridden",
	}
	getenv := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	cfg, err := Load(Options{Getenv: getenv})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OpenClaw.HookToken != "secret-value" {
		t.Fatalf("secret file should win (and be trimmed): %q", cfg.OpenClaw.HookToken)
	}
}

func TestSecretFileWinsOverFile(t *testing.T) {
	path := writeTemp(t, "config.yaml", "openclaw:\n  hook_token: \"from-file\"\n")
	secretPath := writeTemp(t, "tok", "from-secret")
	getenv := func(k string) (string, bool) {
		if k == "OPENCLAW_HOOK_TOKEN_FILE" {
			return secretPath, true
		}
		return "", false
	}
	cfg, err := Load(Options{ConfigPath: path, Getenv: getenv})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OpenClaw.HookToken != "from-secret" {
		t.Fatalf("secret should beat file: %q", cfg.OpenClaw.HookToken)
	}
}

func TestCoercions(t *testing.T) {
	if v, ok := ToFloat("42.5"); !ok || v != 42.5 {
		t.Fatalf("ToFloat string: %v %v", v, ok)
	}
	if v, ok := ToInt("7"); !ok || v != 7 {
		t.Fatalf("ToInt string: %v", v)
	}
	if v, ok := ToBool("true"); !ok || !v {
		t.Fatalf("ToBool: %v", v)
	}
	s, ok := ToSlice("a, b,c")
	if !ok || len(s) != 3 || s[1] != "b" {
		t.Fatalf("ToSlice: %v", s)
	}
	s2, ok := ToSlice([]any{"x", "y"})
	if !ok || len(s2) != 2 {
		t.Fatalf("ToSlice any: %v", s2)
	}
}
