// Package gq implements a priority-aware admission-control proxy for SGLang.
// It sits between callers and the LLM endpoint, scheduling requests across
// priority lanes with DRR fairness, aging promotion, and circuit breaking.
package gq

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config holds all gq proxy configuration, loaded from TOML.
type Config struct {
	// Network
	BindAddr        string   `toml:"bind_addr"`
	MetricsAddr     string   `toml:"metrics_addr"`
	Upstream        string   `toml:"upstream"`
	UpstreamTimeout Duration `toml:"upstream_timeout"`

	// Admission
	MaxSlots      int `toml:"max_slots"`
	QueueMaxDepth int `toml:"queue_max_depth"`

	// Scheduling
	QuantumTokens int      `toml:"quantum_tokens"`
	TiebreakScanK int      `toml:"tiebreak_scan_k"`
	DrainTimeout  Duration `toml:"drain_timeout"`

	// Priority mapping when header absent: role -> priority string.
	DefaultPriority map[string]string `toml:"default_priority"`

	// Per-lane timeouts (enqueue-to-dispatch).
	LaneTimeouts LaneTimeoutConfig `toml:"lane_timeouts"`

	// Aging promotion thresholds.
	Aging AgingConfig `toml:"aging"`

	// Observability.
	Stats StatsConfig `toml:"stats"`

	// Upstream circuit breaker.
	UpstreamBreaker BreakerConfig `toml:"upstream_breaker"`

	// Rate limits.
	RateLimit RateLimitConfig `toml:"rate_limit"`
}

// LaneTimeoutConfig holds per-priority queue timeouts.
type LaneTimeoutConfig struct {
	High   Duration `toml:"high"`
	Normal Duration `toml:"normal"`
	Low    Duration `toml:"low"`
}

// AgingConfig holds aging promotion thresholds.
type AgingConfig struct {
	NormalToHigh Duration `toml:"normal_to_high"`
	LowToNormal  Duration `toml:"low_to_normal"`
}

// StatsConfig holds observability settings.
type StatsConfig struct {
	Window     Duration `toml:"window"`
	LogSlowReq Duration `toml:"log_slow_req"`
}

// BreakerConfig holds upstream circuit breaker settings.
type BreakerConfig struct {
	OpenAfterFailures int      `toml:"open_after_failures"`
	OpenCooldown      Duration `toml:"open_cooldown"`
	MaxCooldown       Duration `toml:"max_cooldown"`
}

// RateLimitConfig holds per-caller rate limiting settings.
type RateLimitConfig struct {
	OrchRPS int `toml:"orch_rps"`
	TempRPS int `toml:"temp_rps"`
}

// LaneTimeout returns the queue timeout for a given priority.
func (c *Config) LaneTimeout(p Priority) time.Duration {
	switch p {
	case High:
		return c.LaneTimeouts.High.Duration
	case Normal:
		return c.LaneTimeouts.Normal.Duration
	case Low:
		return c.LaneTimeouts.Low.Duration
	default:
		return c.LaneTimeouts.Normal.Duration
	}
}

// AgingThreshold returns the aging promotion threshold for a given priority.
// High priority items do not age (they are already at the top).
func (c *Config) AgingThreshold(p Priority) time.Duration {
	switch p {
	case Normal:
		return c.Aging.NormalToHigh.Duration
	case Low:
		return c.Aging.LowToNormal.Duration
	default:
		return 0
	}
}

// DefaultPriorityFor returns the default priority for a caller role.
func (c *Config) DefaultPriorityFor(role string) Priority {
	role = strings.ToLower(role)
	if s, ok := c.DefaultPriority[role]; ok {
		return ParsePriority(s)
	}
	if s, ok := c.DefaultPriority["*"]; ok {
		return ParsePriority(s)
	}
	return Low
}

// Defaults returns a Config with all fields set to production defaults.
func Defaults() *Config {
	return &Config{
		BindAddr:        "127.0.0.1:8090",
		MetricsAddr:     "127.0.0.1:8091",
		Upstream:        "http://localhost:8081",
		UpstreamTimeout: Duration{600 * time.Second},
		MaxSlots:        4,
		QueueMaxDepth:   128,
		QuantumTokens:   8192,
		TiebreakScanK:   4,
		DrainTimeout:    Duration{30 * time.Second},
		DefaultPriority: map[string]string{
			"classifier": "high",
			"prep":       "high",
			"reviewer":   "normal",
			"coder":      "normal",
			"fixer":      "normal",
			"*":          "low",
		},
		LaneTimeouts: LaneTimeoutConfig{
			High:   Duration{60 * time.Second},
			Normal: Duration{300 * time.Second},
			Low:    Duration{900 * time.Second},
		},
		Aging: AgingConfig{
			NormalToHigh: Duration{30 * time.Second},
			LowToNormal:  Duration{90 * time.Second},
		},
		Stats: StatsConfig{
			Window:     Duration{5 * time.Minute},
			LogSlowReq: Duration{30 * time.Second},
		},
		UpstreamBreaker: BreakerConfig{
			OpenAfterFailures: 5,
			OpenCooldown:      Duration{10 * time.Second},
			MaxCooldown:       Duration{5 * time.Minute},
		},
		RateLimit: RateLimitConfig{
			OrchRPS: 20,
			TempRPS: 5,
		},
	}
}

// LoadConfig reads TOML config from path, merging over defaults.
// If path is empty, returns defaults only. Environment variables
// with GQ_ prefix override individual fields.
func LoadConfig(path string) (*Config, error) {
	cfg := Defaults()
	if path != "" {
		if _, err := toml.DecodeFile(path, cfg); err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("load config %s: %w", path, err)
			}
			// File not found is OK — use defaults.
		}
	}
	applyEnvOverrides(cfg)
	return cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("GQ_BIND_ADDR"); v != "" {
		cfg.BindAddr = v
	}
	if v := os.Getenv("GQ_METRICS_ADDR"); v != "" {
		cfg.MetricsAddr = v
	}
	if v := os.Getenv("GQ_UPSTREAM"); v != "" {
		cfg.Upstream = v
	}
	if v := os.Getenv("GQ_MAX_SLOTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxSlots = n
		}
	}
	if v := os.Getenv("GQ_QUEUE_MAX_DEPTH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.QueueMaxDepth = n
		}
	}
}

// Duration wraps time.Duration for TOML unmarshalling of strings like "30s".
type Duration struct {
	time.Duration
}

// UnmarshalText implements encoding.TextUnmarshaler for TOML duration strings.
func (d *Duration) UnmarshalText(b []byte) error {
	dur, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = dur
	return nil
}

// MarshalText implements encoding.TextMarshaler for TOML output.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(d.Duration.String()), nil
}
