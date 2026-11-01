package config

import (
	"strings"
	"testing"
	"time"
)

const testUpstream = "http://localhost:8080"

func envLookup(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

// env returns an environment with UPSTREAM set to testUpstream plus the given overrides.
func env(kv map[string]string) map[string]string {
	m := map[string]string{"UPSTREAM": testUpstream}
	for k, v := range kv {
		m[k] = v
	}
	return m
}

// loadCase is one Load call: wantErr is matched as a substring of the error, check
// asserts fields of the config that came back when no error is expected.
type loadCase struct {
	name    string
	env     map[string]string
	wantErr string
	check   func(*testing.T, *Config)
}

func runLoadCases(t *testing.T, cases []loadCase) {
	t.Helper()
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(envLookup(tt.env))
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("expected error containing %q, got %q", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

func wantUpstreams(want ...string) func(*testing.T, *Config) {
	return func(t *testing.T, cfg *Config) {
		t.Helper()
		if len(cfg.Upstreams) != len(want) {
			t.Fatalf("expected %d upstreams, got %d", len(want), len(cfg.Upstreams))
		}
		for i, u := range want {
			if got := cfg.Upstreams[i].String(); got != u {
				t.Errorf("expected upstream[%d] %q, got %q", i, u, got)
			}
		}
	}
}

func wantDelay(minVal, maxVal time.Duration) func(*testing.T, *Config) {
	return func(t *testing.T, cfg *Config) {
		t.Helper()
		if cfg.DelayMin != minVal || cfg.DelayMax != maxVal {
			t.Errorf("expected delay [%v, %v], got [%v, %v]", minVal, maxVal, cfg.DelayMin, cfg.DelayMax)
		}
	}
}

func wantFactor(minVal, maxVal float64) func(*testing.T, *Config) {
	return func(t *testing.T, cfg *Config) {
		t.Helper()
		if cfg.EscalateFactorMin != minVal || cfg.EscalateFactorMax != maxVal {
			t.Errorf("expected factor [%v, %v], got [%v, %v]", minVal, maxVal, cfg.EscalateFactorMin, cfg.EscalateFactorMax)
		}
	}
}

func wantEndpoints(want ...string) func(*testing.T, *Config) {
	return func(t *testing.T, cfg *Config) {
		t.Helper()
		if len(cfg.Endpoints) != len(want) {
			t.Fatalf("expected endpoints %v, got %v", want, cfg.Endpoints)
		}
		for i, ep := range want {
			if cfg.Endpoints[i] != ep {
				t.Errorf("expected endpoint[%d] %q, got %q", i, ep, cfg.Endpoints[i])
			}
		}
	}
}

func wantQueueSize(want int) func(*testing.T, *Config) {
	return func(t *testing.T, cfg *Config) {
		t.Helper()
		if cfg.QueueSize != want {
			t.Errorf("expected QueueSize %d, got %d", want, cfg.QueueSize)
		}
	}
}

func TestLoad_Upstreams(t *testing.T) {
	runLoadCases(t, []loadCase{
		{name: "single http upstream", env: env(map[string]string{"UPSTREAM": "http://localhost:8080"}), check: wantUpstreams("http://localhost:8080")},
		{name: "single https upstream", env: env(map[string]string{"UPSTREAM": "https://example.com"}), check: wantUpstreams("https://example.com")},
		{name: "multiple upstreams", env: env(map[string]string{"UPSTREAM": "http://localhost:8080, https://example.com"}), check: wantUpstreams("http://localhost:8080", "https://example.com")},
		{name: "whitespace around values", env: env(map[string]string{"UPSTREAM": "  http://localhost:8080  ", "PORT": "  9000  "}), check: func(t *testing.T, cfg *Config) {
			wantUpstreams("http://localhost:8080")(t, cfg)
			if cfg.Port != 9000 {
				t.Errorf("expected Port 9000, got %d", cfg.Port)
			}
		}},
		{name: "missing upstream", env: env(map[string]string{"UPSTREAM": ""}), wantErr: "UPSTREAM is required"},
		{name: "empty upstreams (only whitespace)", env: env(map[string]string{"UPSTREAM": "  ,  ,  "}), wantErr: "UPSTREAM is required"},
		{name: "invalid scheme", env: env(map[string]string{"UPSTREAM": "ftp://example.com"}), wantErr: "scheme must be http or https"},
		{name: "malformed URL", env: env(map[string]string{"UPSTREAM": "://invalid-url"}), wantErr: "invalid UPSTREAM"},
	})
}

func TestLoad_DefaultValues(t *testing.T) {
	cfg, err := Load(envLookup(env(nil)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Port != DefaultPort {
		t.Errorf("expected default Port %d, got %d", DefaultPort, cfg.Port)
	}
	if cfg.UpstreamTimeout != DefaultUpstreamTimeout*time.Second {
		t.Errorf("expected default UpstreamTimeout %ds, got %v", DefaultUpstreamTimeout, cfg.UpstreamTimeout)
	}
	if cfg.DelayMin != 0 || cfg.DelayMax != 0 {
		t.Errorf("expected default delay 0, got [%v, %v]", cfg.DelayMin, cfg.DelayMax)
	}
	if cfg.EscalateMaxCount != DefaultEscalateMaxCount {
		t.Errorf("expected default EscalateMaxCount %d, got %d", DefaultEscalateMaxCount, cfg.EscalateMaxCount)
	}
	if cfg.EscalateFactorMin != DefaultEscalateFactorMin || cfg.EscalateFactorMax != DefaultEscalateFactorMax {
		t.Errorf("expected default factor [%v, %v], got [%v, %v]",
			DefaultEscalateFactorMin, DefaultEscalateFactorMax, cfg.EscalateFactorMin, cfg.EscalateFactorMax)
	}
	if len(cfg.Endpoints) != 1 || cfg.Endpoints[0] != "/" {
		t.Errorf("expected default Endpoints [\"/\"], got %v", cfg.Endpoints)
	}
}

func TestLoad_DelayRange(t *testing.T) {
	runLoadCases(t, []loadCase{
		{name: "constant delay", env: env(map[string]string{"DELAY": "5"}), check: wantDelay(5*time.Second, 5*time.Second)},
		{name: "range delay", env: env(map[string]string{"DELAY": "0.5:2"}), check: wantDelay(500*time.Millisecond, 2*time.Second)},
		{name: "max less than min clamp", env: env(map[string]string{"DELAY": "5:3"}), check: wantDelay(5*time.Second, 5*time.Second)},
		{name: "default delay", env: env(nil), check: wantDelay(0, 0)},
		{name: "invalid delay", env: env(map[string]string{"DELAY": "foo"}), wantErr: "DELAY must be a number"},
		{name: "multiple colons", env: env(map[string]string{"DELAY": "1:2:3"}), wantErr: "must have at most one colon"},
		{name: "negative delay", env: env(map[string]string{"DELAY": "-1"}), wantErr: "must not be negative"},
		{name: "negative delay max", env: env(map[string]string{"DELAY": "1:-2"}), wantErr: "max must not be negative"},
	})
}

func TestLoad_EscalateFactor(t *testing.T) {
	runLoadCases(t, []loadCase{
		{name: "constant factor", env: env(map[string]string{"ESCALATE_FACTOR": "1.5"}), check: wantFactor(1.5, 1.5)},
		{name: "range factor", env: env(map[string]string{"ESCALATE_FACTOR": "1.5:2.0"}), check: wantFactor(1.5, 2.0)},
		{name: "default factor", env: env(nil), check: wantFactor(1.5, 2.0)},
		{name: "max less than min clamp", env: env(map[string]string{"ESCALATE_FACTOR": "2.0:1.5"}), check: wantFactor(2.0, 2.0)},
		{name: "multiple colons", env: env(map[string]string{"ESCALATE_FACTOR": "1.5:2.0:3.0"}), wantErr: "must have at most one colon"},
		{name: "invalid factor", env: env(map[string]string{"ESCALATE_FACTOR": "foo"}), wantErr: "ESCALATE_FACTOR must be a number"},
		{name: "negative factor", env: env(map[string]string{"ESCALATE_FACTOR": "-1.5"}), wantErr: "ESCALATE_FACTOR must not be negative"},
		{name: "negative factor max", env: env(map[string]string{"ESCALATE_FACTOR": "1.5:-2"}), wantErr: "ESCALATE_FACTOR max must not be negative"},
	})
}

func TestLoad_EdgeCases(t *testing.T) {
	runLoadCases(t, []loadCase{
		{name: "invalid upstream timeout", env: env(map[string]string{"UPSTREAM_TIMEOUT": "abc"}), wantErr: "UPSTREAM_TIMEOUT must be a number"},
		{name: "invalid port", env: env(map[string]string{"PORT": "not-a-number"}), wantErr: "PORT must be an integer"},
		{name: "invalid escalate after", env: env(map[string]string{"ESCALATE_AFTER": "xyz"}), wantErr: "ESCALATE_AFTER must be an integer"},
		{name: "escalate after value 1 too low", env: env(map[string]string{"ESCALATE_AFTER": "1"}), wantErr: "ESCALATE_AFTER must be greater than 1"},
		{name: "escalate after value 2 valid", env: env(map[string]string{"ESCALATE_AFTER": "2"}), check: func(t *testing.T, cfg *Config) {
			if cfg.EscalateAfter != 2 {
				t.Errorf("expected EscalateAfter 2, got %d", cfg.EscalateAfter)
			}
		}},
		{name: "escalate after value 0 disabled valid", env: env(map[string]string{"ESCALATE_AFTER": "0"}), check: func(t *testing.T, cfg *Config) {
			if cfg.EscalateAfter != 0 {
				t.Errorf("expected EscalateAfter 0, got %d", cfg.EscalateAfter)
			}
		}},
		{name: "invalid escalate max count", env: env(map[string]string{"ESCALATE_MAX_COUNT": "xyz"}), wantErr: "ESCALATE_MAX_COUNT must be an integer"},
		{name: "invalid queue size", env: env(map[string]string{"QUEUE_SIZE": "abc"}), wantErr: "QUEUE_SIZE must be an integer"},
	})
}

func TestLoad_MaxWait(t *testing.T) {
	runLoadCases(t, []loadCase{
		{name: "valid max wait", env: env(map[string]string{"MAX_WAIT": "30"}), check: func(t *testing.T, cfg *Config) {
			if cfg.MaxWait != 30*time.Second {
				t.Errorf("expected MaxWait 30s, got %v", cfg.MaxWait)
			}
		}},
		{name: "invalid max wait", env: env(map[string]string{"MAX_WAIT": "invalid"}), wantErr: "MAX_WAIT must be a number"},
	})
}

func TestLoad_EndpointsEdgeCases(t *testing.T) {
	runLoadCases(t, []loadCase{
		{name: "empty endpoints defaults to root", env: env(map[string]string{"ENDPOINTS": ""}), check: wantEndpoints("/")},
		{name: "whitespace only endpoints defaults to root", env: env(map[string]string{"ENDPOINTS": "   "}), check: wantEndpoints("/")},
		{name: "single endpoint without slash", env: env(map[string]string{"ENDPOINTS": "api"}), check: wantEndpoints("api")},
		{name: "endpoint with trailing slash", env: env(map[string]string{"ENDPOINTS": "/api/"}), check: wantEndpoints("/api")},
		{name: "multiple endpoints", env: env(map[string]string{"ENDPOINTS": "/api, /search"}), check: wantEndpoints("/api", "/search")},
	})
}

func TestLoad_QueueSizeEdgeCases(t *testing.T) {
	runLoadCases(t, []loadCase{
		{name: "valid queue size", env: env(map[string]string{"QUEUE_SIZE": "5000"}), check: wantQueueSize(5000)},
		{name: "zero queue size uses default", env: env(map[string]string{"QUEUE_SIZE": "0"}), check: wantQueueSize(DefaultQueueSize)},
		{name: "negative queue size uses default", env: env(map[string]string{"QUEUE_SIZE": "-100"}), check: wantQueueSize(DefaultQueueSize)},
		{name: "small positive queue size", env: env(map[string]string{"QUEUE_SIZE": "50"}), check: wantQueueSize(50)},
		{name: "minimum queue size", env: env(map[string]string{"QUEUE_SIZE": "1"}), check: wantQueueSize(1)},
	})
}

func TestEnvSeconds(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    time.Duration
		wantErr string
	}{
		{name: "empty returns default", value: "", want: 5 * time.Second},
		{name: "integer seconds", value: "10", want: 10 * time.Second},
		{name: "float seconds", value: "0.5", want: 500 * time.Millisecond},
		{name: "large value", value: "3600", want: 3600 * time.Second},
		{name: "zero", value: "0", want: 0},
		{name: "invalid", value: "invalid", wantErr: "TEST_TIMEOUT must be a number"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := envSeconds("TEST_TIMEOUT", 5.0, envLookup(map[string]string{"TEST_TIMEOUT": tt.value}))
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("expected duration %v, got %v", tt.want, got)
			}
		})
	}
}

func TestMatchesEndpoints(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		endpoints []string
		want      bool
	}{
		{name: "exact match", path: "/search", endpoints: []string{"/search"}, want: true},
		{name: "prefix match", path: "/search/foo/bar", endpoints: []string{"/search"}, want: true},
		{name: "no match - similar prefix", path: "/searches", endpoints: []string{"/search"}, want: false},
		{name: "root endpoint matches everything", path: "/anything", endpoints: []string{"/"}, want: true},
		{name: "multiple endpoints - match second", path: "/api/v1/users", endpoints: []string{"/search", "/api"}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchesEndpoints(tt.path, tt.endpoints); got != tt.want {
				t.Errorf("MatchesEndpoints(%q, %v) = %v, want %v", tt.path, tt.endpoints, got, tt.want)
			}
		})
	}
}
