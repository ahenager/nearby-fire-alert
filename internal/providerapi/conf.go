package providerapi

import (
	"time"

	"github.com/ahenager/nearby-fire-alert/internal/config"
)

// Conf is a small typed accessor for a provider's config block. It resolves a
// single value through the three highest tiers in precedence order:
//
//	secret file / env (via get)  >  config-file block (cfg)  >  default
//
// The lowest tier (built-in code default) is the provided default argument.
type Conf struct {
	cfg map[string]any
	get func(string) (string, bool)
}

// NewConf wraps a provider config block and the resolved env+secret lookup.
func NewConf(cfg map[string]any, get func(string) (string, bool)) *Conf {
	return &Conf{cfg: cfg, get: get}
}

// String resolves a string value.
func (c *Conf) String(envKey, cfgKey, def string) string {
	if v, ok := c.get(envKey); ok {
		return v
	}
	if v, ok := c.cfg[cfgKey]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

// Int resolves an integer value.
func (c *Conf) Int(envKey, cfgKey string, def int) int {
	if v, ok := c.get(envKey); ok {
		if i, ok := config.ToInt(v); ok {
			return i
		}
	}
	if v, ok := c.cfg[cfgKey]; ok {
		if i, ok := config.ToInt(v); ok {
			return i
		}
	}
	return def
}

// Float resolves a float value.
func (c *Conf) Float(envKey, cfgKey string, def float64) float64 {
	if v, ok := c.get(envKey); ok {
		if f, ok := config.ToFloat(v); ok {
			return f
		}
	}
	if v, ok := c.cfg[cfgKey]; ok {
		if f, ok := config.ToFloat(v); ok {
			return f
		}
	}
	return def
}

// Bool resolves a boolean value.
func (c *Conf) Bool(envKey, cfgKey string, def bool) bool {
	if v, ok := c.get(envKey); ok {
		if b, ok := config.ToBool(v); ok {
			return b
		}
	}
	if v, ok := c.cfg[cfgKey]; ok {
		if b, ok := config.ToBool(v); ok {
			return b
		}
	}
	return def
}

// Duration resolves a duration value (seconds number or duration string).
func (c *Conf) Duration(envKey, cfgKey string, def time.Duration) time.Duration {
	if v, ok := c.get(envKey); ok {
		if d, ok := config.ToDuration(v); ok {
			return d
		}
	}
	if v, ok := c.cfg[cfgKey]; ok {
		if d, ok := config.ToDuration(v); ok {
			return d
		}
	}
	return def
}

// Slice resolves a string slice (comma-separated or a YAML list).
func (c *Conf) Slice(envKey, cfgKey string, def []string) []string {
	if v, ok := c.get(envKey); ok {
		if s, ok := config.ToSlice(v); ok {
			return s
		}
	}
	if v, ok := c.cfg[cfgKey]; ok {
		if s, ok := config.ToSlice(v); ok {
			return s
		}
	}
	return def
}
