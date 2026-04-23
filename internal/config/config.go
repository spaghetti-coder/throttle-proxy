package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port             int
	Upstreams        []*url.URL
	UpstreamTimeout  time.Duration
	DelayMin         time.Duration
	DelayMax         time.Duration
	MaxWait          time.Duration
	EscalateAfter    int
	EscalateMaxCount int
	Endpoints        []string
	QueueSize        int
}

func Load() (*Config, error) {
	cfg := &Config{}

	var err error

	cfg.Port, err = envInt("PORT", 8080)
	if err != nil {
		return nil, err
	}

	upstreamRaw := strings.TrimSpace(os.Getenv("UPSTREAM"))
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

	cfg.UpstreamTimeout, err = envSeconds("UPSTREAM_TIMEOUT", 5)
	if err != nil {
		return nil, err
	}

	cfg.DelayMin, err = envSeconds("DELAY_MIN", 0)
	if err != nil {
		return nil, err
	}

	cfg.DelayMax, err = envSeconds("DELAY_MAX", 0)
	if err != nil {
		return nil, err
	}

	if cfg.DelayMax < cfg.DelayMin {
		cfg.DelayMax = cfg.DelayMin
	}

	cfg.MaxWait, err = envSeconds("MAX_WAIT", 0)
	if err != nil {
		return nil, err
	}

	cfg.EscalateAfter, err = envInt("ESCALATE_DELAY_AFTER", 0)
	if err != nil {
		return nil, err
	}

	cfg.EscalateMaxCount, err = envInt("ESCALATE_DELAY_MAX_COUNT", 3)
	if err != nil {
		return nil, err
	}

	endpoints := strings.TrimSpace(os.Getenv("ENDPOINTS"))
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

	cfg.QueueSize, err = envInt("QUEUE_SIZE", 10000)
	if err != nil {
		return nil, err
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 10000
	}
	if cfg.QueueSize < 100 {
		cfg.QueueSize = 100
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

func envInt(name string, defaultVal int) (int, error) {
	s := strings.TrimSpace(os.Getenv(name))
	if s == "" {
		return defaultVal, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return v, nil
}

func envSeconds(name string, defaultVal float64) (time.Duration, error) {
	s := strings.TrimSpace(os.Getenv(name))
	if s == "" {
		return time.Duration(defaultVal * float64(time.Second)), nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number: %w", name, err)
	}
	return time.Duration(v * float64(time.Second)), nil
}
