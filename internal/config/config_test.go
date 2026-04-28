package config

import (
	"strings"
	"testing"
	"time"
)

// TestLoad_RequiredEnv tests that UPSTREAM is required
func TestLoad_RequiredEnv(t *testing.T) {
	_, err := Load(func(string) string { return "" })
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
			cfg, err := Load(func(k string) string { return tt.env[k] })
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
	cfg, err := Load(func(k string) string {
		env := map[string]string{"UPSTREAM": "http://localhost:8080"}
		return env[k]
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Port != 8080 {
		t.Errorf("expected default Port 8080, got %d", cfg.Port)
	}
	if cfg.UpstreamTimeout != 5*time.Second {
		t.Errorf("expected default UpstreamTimeout 5s, got %v", cfg.UpstreamTimeout)
	}
	if cfg.DelayMin != 0 {
		t.Errorf("expected default DelayMin 0, got %v", cfg.DelayMin)
	}
	if cfg.EscalateMaxCount != 3 {
		t.Errorf("expected default EscalateMaxCount 3, got %d", cfg.EscalateMaxCount)
	}
	if len(cfg.Endpoints) != 1 || cfg.Endpoints[0] != "/" {
		t.Errorf("expected default Endpoints [\"/\"], got %v", cfg.Endpoints)
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

// Helper functions
func envLookup(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}
