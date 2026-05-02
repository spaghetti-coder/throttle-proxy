package config

import (
	"strings"
	"testing"
	"time"
)

func envLookup(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

// TestLoad_RequiredEnv tests that UPSTREAM is required
func TestLoad_RequiredEnv(t *testing.T) {
	_, err := Load(envLookup(map[string]string{"UPSTREAM": ""}))
	if err == nil {
		t.Error("expected error for missing UPSTREAM, got nil")
	}
	if err.Error() != "UPSTREAM is required" {
		t.Errorf("expected error 'UPSTREAM is required', got %v", err)
	}
}

// TestLoad_UpstreamParsing tests upstream URL parsing
func TestLoad_UpstreamParsing(t *testing.T) {
	tests := []struct {
		name           string
		env            map[string]string
		wantErr        bool
		wantErrContain string
		wantCount      int
		wantFirst      string
	}{
		{
			name:      "single http upstream",
			env:       map[string]string{"UPSTREAM": "http://localhost:8080"},
			wantErr:   false,
			wantCount: 1,
			wantFirst: "http://localhost:8080",
		},
		{
			name:      "single https upstream",
			env:       map[string]string{"UPSTREAM": "https://example.com"},
			wantErr:   false,
			wantCount: 1,
			wantFirst: "https://example.com",
		},
		{
			name:      "multiple upstreams",
			env:       map[string]string{"UPSTREAM": "http://localhost:8080, https://example.com"},
			wantErr:   false,
			wantCount: 2,
			wantFirst: "http://localhost:8080",
		},
		{
			name:           "invalid scheme",
			env:            map[string]string{"UPSTREAM": "ftp://example.com"},
			wantErr:        true,
			wantErrContain: "scheme must be http or https",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(envLookup(tt.env))
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
					return
				}
				if tt.wantErrContain != "" && !strings.Contains(err.Error(), tt.wantErrContain) {
					t.Errorf("expected error containing %q, got %q", tt.wantErrContain, err.Error())
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if len(cfg.Upstreams) != tt.wantCount {
				t.Errorf("expected %d upstreams, got %d", tt.wantCount, len(cfg.Upstreams))
			}
			if tt.wantCount > 0 && cfg.Upstreams[0].String() != tt.wantFirst {
				t.Errorf("expected first upstream %q, got %q", tt.wantFirst, cfg.Upstreams[0].String())
			}
		})
	}
}

// TestLoad_DefaultValues tests default values
func TestLoad_DefaultValues(t *testing.T) {
	cfg, err := Load(envLookup(map[string]string{"UPSTREAM": "http://localhost:8080"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Port != DefaultPort {
		t.Errorf("expected default Port %d, got %d", DefaultPort, cfg.Port)
	}
	if cfg.UpstreamTimeout != DefaultUpstreamTimeout*time.Second {
		t.Errorf("expected default UpstreamTimeout %ds, got %v", DefaultUpstreamTimeout, cfg.UpstreamTimeout)
	}
	if cfg.DelayMin != 0 {
		t.Errorf("expected default DelayMin %d, got %v", DefaultDelayMin, cfg.DelayMin)
	}
	if cfg.DelayMax != 0 {
		t.Errorf("expected default DelayMax %d, got %v", DefaultDelayMax, cfg.DelayMax)
	}
	if cfg.EscalateMaxCount != DefaultEscalateMaxCount {
		t.Errorf("expected default EscalateMaxCount %d, got %d", DefaultEscalateMaxCount, cfg.EscalateMaxCount)
	}
	if cfg.EscalateFactorMin != DefaultEscalateFactorMin {
		t.Errorf("expected default EscalateFactorMin %f, got %f", DefaultEscalateFactorMin, cfg.EscalateFactorMin)
	}
	if cfg.EscalateFactorMax != DefaultEscalateFactorMax {
		t.Errorf("expected default EscalateFactorMax %f, got %f", DefaultEscalateFactorMax, cfg.EscalateFactorMax)
	}
	if len(cfg.Endpoints) != 1 || cfg.Endpoints[0] != "/" {
		t.Errorf("expected default Endpoints [\"/\"], got %v", cfg.Endpoints)
	}
}

// TestLoad_DelayRange tests DELAY variable parsing
func TestLoad_DelayRange(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		wantMin    time.Duration
		wantMax    time.Duration
		wantErr    bool
		wantErrStr string
	}{
		{
			name:    "constant delay",
			env:     map[string]string{"UPSTREAM": "http://localhost:8080", "DELAY": "5"},
			wantMin: 5 * time.Second,
			wantMax: 5 * time.Second,
		},
		{
			name:    "range delay",
			env:     map[string]string{"UPSTREAM": "http://localhost:8080", "DELAY": "0.5:2"},
			wantMin: 500 * time.Millisecond,
			wantMax: 2 * time.Second,
		},
		{
			name:    "max less than min clamp",
			env:     map[string]string{"UPSTREAM": "http://localhost:8080", "DELAY": "5:3"},
			wantMin: 5 * time.Second,
			wantMax: 5 * time.Second,
		},
		{
			name:    "default delay",
			env:     map[string]string{"UPSTREAM": "http://localhost:8080"},
			wantMin: 0,
			wantMax: 0,
		},
		{
			name:       "invalid delay",
			env:        map[string]string{"UPSTREAM": "http://localhost:8080", "DELAY": "foo"},
			wantErr:    true,
			wantErrStr: "DELAY must be a number",
		},
		{
			name:       "multiple colons",
			env:        map[string]string{"UPSTREAM": "http://localhost:8080", "DELAY": "1:2:3"},
			wantErr:    true,
			wantErrStr: "must have at most one colon",
		},
		{
			name:       "negative delay",
			env:        map[string]string{"UPSTREAM": "http://localhost:8080", "DELAY": "-1"},
			wantErr:    true,
			wantErrStr: "must not be negative",
		},
		{
			name:       "negative delay max",
			env:        map[string]string{"UPSTREAM": "http://localhost:8080", "DELAY": "1:-2"},
			wantErr:    true,
			wantErrStr: "must not be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(envLookup(tt.env))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.wantErrStr) {
					t.Errorf("expected error containing %q, got %v", tt.wantErrStr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.DelayMin != tt.wantMin {
				t.Errorf("expected DelayMin %v, got %v", tt.wantMin, cfg.DelayMin)
			}
			if cfg.DelayMax != tt.wantMax {
				t.Errorf("expected DelayMax %v, got %v", tt.wantMax, cfg.DelayMax)
			}
		})
	}
}

// TestLoad_EscalateFactor tests ESCALATE_FACTOR variable parsing
func TestLoad_EscalateFactor(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		wantMin    float64
		wantMax    float64
		wantErr    bool
		wantErrStr string
	}{
		{
			name:    "constant factor",
			env:     map[string]string{"UPSTREAM": "http://localhost:8080", "ESCALATE_FACTOR": "1.5"},
			wantMin: 1.5,
			wantMax: 1.5,
		},
		{
			name:    "range factor",
			env:     map[string]string{"UPSTREAM": "http://localhost:8080", "ESCALATE_FACTOR": "1.5:2.0"},
			wantMin: 1.5,
			wantMax: 2.0,
		},
		{
			name:    "default factor",
			env:     map[string]string{"UPSTREAM": "http://localhost:8080"},
			wantMin: 1.5,
			wantMax: 2.0,
		},
		{
			name:    "max less than min clamp",
			env:     map[string]string{"UPSTREAM": "http://localhost:8080", "ESCALATE_FACTOR": "2.0:1.5"},
			wantMin: 2.0,
			wantMax: 2.0,
		},
		{
			name:       "multiple colons",
			env:        map[string]string{"UPSTREAM": "http://localhost:8080", "ESCALATE_FACTOR": "1.5:2.0:3.0"},
			wantErr:    true,
			wantErrStr: "must have at most one colon",
		},
		{
			name:       "invalid factor",
			env:        map[string]string{"UPSTREAM": "http://localhost:8080", "ESCALATE_FACTOR": "foo"},
			wantErr:    true,
			wantErrStr: "ESCALATE_FACTOR must be a number",
		},
		{
			name:       "negative factor",
			env:        map[string]string{"UPSTREAM": "http://localhost:8080", "ESCALATE_FACTOR": "-1.5"},
			wantErr:    true,
			wantErrStr: "ESCALATE_FACTOR must not be negative",
		},
		{
			name:       "negative factor max",
			env:        map[string]string{"UPSTREAM": "http://localhost:8080", "ESCALATE_FACTOR": "1.5:-2"},
			wantErr:    true,
			wantErrStr: "ESCALATE_FACTOR max must not be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(envLookup(tt.env))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.wantErrStr) {
					t.Errorf("expected error containing %q, got %v", tt.wantErrStr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.EscalateFactorMin != tt.wantMin {
				t.Errorf("expected EscalateFactorMin %v, got %v", tt.wantMin, cfg.EscalateFactorMin)
			}
			if cfg.EscalateFactorMax != tt.wantMax {
				t.Errorf("expected EscalateFactorMax %v, got %v", tt.wantMax, cfg.EscalateFactorMax)
			}
		})
	}
}

// TestEnvSeconds_Empty tests envSeconds returns default when env var is empty
func TestEnvSeconds_Empty(t *testing.T) {
	lookup := envLookup(map[string]string{"TEST_TIMEOUT": ""})
	duration, err := envSeconds("TEST_TIMEOUT", 5.0, lookup)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	expected := 5 * time.Second
	if duration != expected {
		t.Errorf("expected duration %v, got %v", expected, duration)
	}
}

// TestEnvSeconds_Valid tests envSeconds parses valid duration
func TestEnvSeconds_Valid(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		expected time.Duration
	}{
		{
			name:     "integer seconds",
			value:    "10",
			expected: 10 * time.Second,
		},
		{
			name:     "float seconds",
			value:    "0.5",
			expected: 500 * time.Millisecond,
		},
		{
			name:     "large value",
			value:    "3600",
			expected: 3600 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := envLookup(map[string]string{"TEST_TIMEOUT": tt.value})
			duration, err := envSeconds("TEST_TIMEOUT", 5.0, lookup)
			if err != nil {
				t.Errorf("expected no error, got %v", err)
			}
			if duration != tt.expected {
				t.Errorf("expected duration %v, got %v", tt.expected, duration)
			}
		})
	}
}

// TestEnvSeconds_Invalid tests envSeconds returns error for invalid values
func TestEnvSeconds_Invalid(t *testing.T) {
	lookup := envLookup(map[string]string{"TEST_TIMEOUT": "invalid"})
	_, err := envSeconds("TEST_TIMEOUT", 5.0, lookup)
	if err == nil {
		t.Error("expected error for invalid value, got nil")
	}
	if !strings.Contains(err.Error(), "TEST_TIMEOUT must be a number") {
		t.Errorf("expected error containing 'TEST_TIMEOUT must be a number', got %v", err)
	}
}

// TestEnvSeconds_Zero tests envSeconds handles zero
func TestEnvSeconds_Zero(t *testing.T) {
	lookup := envLookup(map[string]string{"TEST_TIMEOUT": "0"})
	duration, err := envSeconds("TEST_TIMEOUT", 5.0, lookup)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if duration != 0 {
		t.Errorf("expected duration 0, got %v", duration)
	}
}

// TestLoad_EdgeCases tests edge cases and error paths
func TestLoad_EdgeCases(t *testing.T) {
	tests := []struct {
		name           string
		env            map[string]string
		wantErr        bool
		wantErrContain string
	}{
		{
			name:           "malformed URL",
			env:            map[string]string{"UPSTREAM": "://invalid-url"},
			wantErr:        true,
			wantErrContain: "invalid UPSTREAM",
		},
		{
			name:           "empty upstreams (only whitespace)",
			env:            map[string]string{"UPSTREAM": "  ,  ,  "},
			wantErr:        true,
			wantErrContain: "UPSTREAM is required",
		},
		{
			name:           "invalid upstream timeout",
			env:            map[string]string{"UPSTREAM": "http://localhost:8080", "UPSTREAM_TIMEOUT": "abc"},
			wantErr:        true,
			wantErrContain: "UPSTREAM_TIMEOUT must be a number",
		},
		{
			name:           "invalid port",
			env:            map[string]string{"UPSTREAM": "http://localhost:8080", "PORT": "not-a-number"},
			wantErr:        true,
			wantErrContain: "PORT must be an integer",
		},
		{
			name:           "invalid escalate after",
			env:            map[string]string{"UPSTREAM": "http://localhost:8080", "ESCALATE_AFTER": "xyz"},
			wantErr:        true,
			wantErrContain: "ESCALATE_AFTER must be an integer",
		},
		{
			name:           "invalid escalate max count",
			env:            map[string]string{"UPSTREAM": "http://localhost:8080", "ESCALATE_MAX_COUNT": "xyz"},
			wantErr:        true,
			wantErrContain: "ESCALATE_MAX_COUNT must be an integer",
		},
		{
			name:           "invalid queue size",
			env:            map[string]string{"UPSTREAM": "http://localhost:8080", "QUEUE_SIZE": "abc"},
			wantErr:        true,
			wantErrContain: "QUEUE_SIZE must be an integer",
		},
		{
			name:           "whitespace around values",
			env:            map[string]string{"UPSTREAM": "  http://localhost:8080  ", "PORT": "  9000  "},
			wantErr:        false,
			wantErrContain: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(envLookup(tt.env))
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
					return
				}
				if tt.wantErrContain != "" && !strings.Contains(err.Error(), tt.wantErrContain) {
					t.Errorf("expected error containing %q, got %q", tt.wantErrContain, err.Error())
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if cfg != nil && tt.name == "whitespace around values" {
				if cfg.Port != 9000 {
					t.Errorf("expected Port 9000, got %d", cfg.Port)
				}
			}
		})
	}
}

// TestLoad_MaxWait tests MAX_WAIT edge cases
func TestLoad_MaxWait(t *testing.T) {
	tests := []struct {
		name       string
		maxWait    string
		wantErr    bool
		wantErrStr string
	}{
		{
			name:    "valid max wait",
			maxWait: "30",
			wantErr: false,
		},
		{
			name:       "invalid max wait",
			maxWait:    "invalid",
			wantErr:    true,
			wantErrStr: "MAX_WAIT must be a number",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{
				"UPSTREAM": "http://localhost:8080",
				"MAX_WAIT": tt.maxWait,
			}
			cfg, err := Load(envLookup(env))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.wantErrStr) {
					t.Errorf("expected error containing %q, got %v", tt.wantErrStr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			_ = cfg
		})
	}
}

// TestLoad_EndpointsEdgeCases tests edge cases in endpoint parsing
func TestLoad_EndpointsEdgeCases(t *testing.T) {
	tests := []struct {
		name         string
		endpoints    string
		wantEndpoints []string
	}{
		{
			name:         "empty endpoints defaults to root",
			endpoints:    "",
			wantEndpoints: []string{"/"},
		},
		{
			name:         "whitespace only endpoints defaults to root",
			endpoints:    "   ",
			wantEndpoints: []string{"/"},
		},
		{
			name:         "single endpoint without slash",
			endpoints:    "api",
			wantEndpoints: []string{"api"},
		},
		{
			name:         "endpoint with trailing slash",
			endpoints:    "/api/",
			wantEndpoints: []string{"/api"},
		},
		{
			name:         "multiple endpoints",
			endpoints:    "/api, /search",
			wantEndpoints: []string{"/api", "/search"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{
				"UPSTREAM":  "http://localhost:8080",
				"ENDPOINTS": tt.endpoints,
			}
			cfg, err := Load(envLookup(env))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(cfg.Endpoints) != len(tt.wantEndpoints) {
				t.Errorf("expected %d endpoints, got %d: %v", len(tt.wantEndpoints), len(cfg.Endpoints), cfg.Endpoints)
				return
			}
			for i, want := range tt.wantEndpoints {
				if cfg.Endpoints[i] != want {
					t.Errorf("expected endpoint[%d] = %q, got %q", i, want, cfg.Endpoints[i])
				}
			}
		})
	}
}

// TestLoad_QueueSizeEdgeCases tests queue size edge cases
func TestLoad_QueueSizeEdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		queueSize string
		wantSize  int
	}{
		{
			name:      "valid queue size",
			queueSize: "5000",
			wantSize:  5000,
		},
		{
			name:      "zero queue size uses default",
			queueSize: "0",
			wantSize:  DefaultQueueSize,
		},
		{
			name:      "negative queue size uses default",
			queueSize: "-100",
			wantSize:  DefaultQueueSize,
		},
		{
			name:      "below minimum queue size",
			queueSize: "50",
			wantSize:  MinQueueSize,
		},
		{
			name:      "minimum queue size",
			queueSize: "100",
			wantSize:  100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{
				"UPSTREAM":   "http://localhost:8080",
				"QUEUE_SIZE": tt.queueSize,
			}
			cfg, err := Load(envLookup(env))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.QueueSize != tt.wantSize {
				t.Errorf("expected QueueSize %d, got %d", tt.wantSize, cfg.QueueSize)
			}
		})
	}
}

// TestMatchesEndpoints tests endpoint prefix matching
func TestMatchesEndpoints(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		endpoints []string
		want      bool
	}{
		{
			name:      "exact match",
			path:      "/search",
			endpoints: []string{"/search"},
			want:      true,
		},
		{
			name:      "prefix match",
			path:      "/search/foo/bar",
			endpoints: []string{"/search"},
			want:      true,
		},
		{
			name:      "no match - similar prefix",
			path:      "/searches",
			endpoints: []string{"/search"},
			want:      false,
		},
		{
			name:      "root endpoint matches everything",
			path:      "/anything",
			endpoints: []string{"/"},
			want:      true,
		},
		{
			name:      "multiple endpoints - match second",
			path:      "/api/v1/users",
			endpoints: []string{"/search", "/api"},
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchesEndpoints(tt.path, tt.endpoints); got != tt.want {
				t.Errorf("MatchesEndpoints(%q, %v) = %v, want %v", tt.path, tt.endpoints, got, tt.want)
			}
		})
	}
}
