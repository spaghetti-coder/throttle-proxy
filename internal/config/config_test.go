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
