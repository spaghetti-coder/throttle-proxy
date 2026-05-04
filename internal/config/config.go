// Package config provides configuration loading for throttle-proxy.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultPort is the default HTTP port for the proxy.
	DefaultPort = 8080
	// DefaultUpstreamTimeout is the default timeout for upstream requests in seconds.
	DefaultUpstreamTimeout = 5
	// DefaultDelayMin is the default minimum delay in seconds.
	DefaultDelayMin = 0
	// DefaultDelayMax is the default maximum delay in seconds.
	DefaultDelayMax = 0
	// DefaultMaxWait is the default maximum wait time for queue in seconds.
	DefaultMaxWait = 0
	// DefaultEscalateAfter is the default number of failures before escalation.
	DefaultEscalateAfter = 0
	// DefaultEscalateMaxCount is the default maximum escalation attempts.
	DefaultEscalateMaxCount = 3
	// DefaultEscalateFactorMin is the default minimum escalation factor.
	DefaultEscalateFactorMin = 1.5
	// DefaultEscalateFactorMax is the default maximum escalation factor.
	DefaultEscalateFactorMax = 2.0
	// DefaultQueueSize is the default request queue size.
	DefaultQueueSize = 100
	// MinQueueSize is the minimum allowed queue size.
	MinQueueSize = 1
)

// Config holds all configuration for throttle-proxy.
type Config struct {
	Port              int
	Upstreams         []*url.URL
	UpstreamTimeout   time.Duration
	DelayMin          time.Duration
	DelayMax          time.Duration
	MaxWait           time.Duration
	EscalateAfter     int
	EscalateMaxCount  int
	EscalateFactorMin float64
	EscalateFactorMax float64
	Endpoints         []string
	QueueSize         int
}

// Load parses configuration from environment variables.
func Load(lookup func(string) string) (*Config, error) {
	if lookup == nil {
		lookup = os.Getenv
	}

	cfg := &Config{}

	var err error

	cfg.Port, err = envInt("PORT", DefaultPort, lookup)
	if err != nil {
		return nil, err
	}

	upstreamRaw := strings.TrimSpace(lookup("UPSTREAM"))
	if upstreamRaw == "" {
		return nil, fmt.Errorf("UPSTREAM is required")
	}
	for _, raw := range strings.Split(upstreamRaw, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, parseErr := url.Parse(raw)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid UPSTREAM %q: %w", raw, parseErr)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("invalid UPSTREAM %q: scheme must be http or https", raw)
		}
		cfg.Upstreams = append(cfg.Upstreams, u)
	}
	if len(cfg.Upstreams) == 0 {
		return nil, fmt.Errorf("UPSTREAM is required")
	}

	cfg.UpstreamTimeout, err = envSeconds("UPSTREAM_TIMEOUT", DefaultUpstreamTimeout, lookup)
	if err != nil {
		return nil, err
	}

	cfg.DelayMin, cfg.DelayMax, err = envSecondsRange("DELAY", DefaultDelayMin, DefaultDelayMax, lookup)
	if err != nil {
		return nil, err
	}

	if cfg.DelayMax < cfg.DelayMin {
		cfg.DelayMax = cfg.DelayMin
	}

	cfg.MaxWait, err = envSeconds("MAX_WAIT", DefaultMaxWait, lookup)
	if err != nil {
		return nil, err
	}

	cfg.EscalateAfter, err = envInt("ESCALATE_AFTER", DefaultEscalateAfter, lookup)
	if err != nil {
		return nil, err
	}
	if cfg.EscalateAfter > 0 && cfg.EscalateAfter < 2 {
		return nil, fmt.Errorf("ESCALATE_AFTER must be greater than 1 (or 0 to disable)")
	}

	cfg.EscalateMaxCount, err = envInt("ESCALATE_MAX_COUNT", DefaultEscalateMaxCount, lookup)
	if err != nil {
		return nil, err
	}

	cfg.EscalateFactorMin, cfg.EscalateFactorMax, err = envFloatRange("ESCALATE_FACTOR", DefaultEscalateFactorMin, DefaultEscalateFactorMax, lookup)
	if err != nil {
		return nil, err
	}

	endpoints := strings.TrimSpace(lookup("ENDPOINTS"))
	if endpoints == "" {
		cfg.Endpoints = []string{"/"}
	} else {
		for _, ep := range strings.Split(endpoints, ",") {
			ep = strings.TrimSpace(ep)
			ep = strings.TrimRight(ep, "/")
			if ep == "" {
				ep = "/"
			}
			cfg.Endpoints = append(cfg.Endpoints, ep)
		}
	}

	cfg.QueueSize, err = envInt("QUEUE_SIZE", DefaultQueueSize, lookup)
	if err != nil {
		return nil, err
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = DefaultQueueSize
	}
	if cfg.QueueSize < MinQueueSize {
		cfg.QueueSize = MinQueueSize
	}

	return cfg, nil
}

// MatchesEndpoints returns true if path matches any configured endpoint prefix.
// "/search" matches "/search" and "/search/foo" but NOT "/searches".
func MatchesEndpoints(path string, endpoints []string) bool {
	for _, ep := range endpoints {
		if ep == "/" {
			return true
		}
		if path == ep || strings.HasPrefix(path, ep+"/") {
			return true
		}
	}
	return false
}

func envInt(name string, defaultVal int, lookup func(string) string) (int, error) {
	s := strings.TrimSpace(lookup(name))
	if s == "" {
		return defaultVal, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return v, nil
}

func envSeconds(name string, defaultVal float64, lookup func(string) string) (time.Duration, error) {
	s := strings.TrimSpace(lookup(name))
	if s == "" {
		return time.Duration(defaultVal * float64(time.Second)), nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number: %w", name, err)
	}
	return time.Duration(v * float64(time.Second)), nil
}

func envSecondsRange(name string, defaultMin, defaultMax float64, lookup func(string) string) (minVal, maxVal time.Duration, err error) {
	s := strings.TrimSpace(lookup(name))
	if s == "" {
		return time.Duration(defaultMin * float64(time.Second)), time.Duration(defaultMax * float64(time.Second)), nil
	}
	parts := strings.Split(s, ":")
	if len(parts) > 2 {
		return 0, 0, fmt.Errorf("%s must have at most one colon", name)
	}
	fmin, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("%s must be a number: %w", name, err)
	}
	if fmin < 0 {
		return 0, 0, fmt.Errorf("%s must not be negative", name)
	}
	fmax := fmin
	if len(parts) > 1 {
		fmax, err = strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err != nil {
			return 0, 0, fmt.Errorf("%s max must be a number: %w", name, err)
		}
		if fmax < 0 {
			return 0, 0, fmt.Errorf("%s max must not be negative", name)
		}
	}
	if fmax < fmin {
		fmax = fmin
	}
	return time.Duration(fmin * float64(time.Second)), time.Duration(fmax * float64(time.Second)), nil
}

func envFloatRange(name string, defaultMin, defaultMax float64, lookup func(string) string) (minVal, maxVal float64, err error) {
	s := strings.TrimSpace(lookup(name))
	if s == "" {
		return defaultMin, defaultMax, nil
	}
	parts := strings.Split(s, ":")
	if len(parts) > 2 {
		return 0, 0, fmt.Errorf("%s must have at most one colon", name)
	}
	minVal, err = strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("%s must be a number: %w", name, err)
	}
	if minVal < 0 {
		return 0, 0, fmt.Errorf("%s must not be negative", name)
	}
	maxVal = minVal
	if len(parts) > 1 {
		maxVal, err = strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err != nil {
			return 0, 0, fmt.Errorf("%s max must be a number: %w", name, err)
		}
		if maxVal < 0 {
			return 0, 0, fmt.Errorf("%s max must not be negative", name)
		}
	}
	if maxVal < minVal {
		maxVal = minVal
	}
	return minVal, maxVal, nil
}
