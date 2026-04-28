package config

import (
	"os"
	"testing"
	"time"
)

// TestLoad_RequiredEnv tests that UPSTREAM is required
func TestLoad_RequiredEnv(t *testing.T) {
	origUpstream := os.Getenv("UPSTREAM")
	defer os.Setenv("UPSTREAM", origUpstream)

	os.Unsetenv("UPSTREAM")

	_, err := Load()
	if err == nil {
		t.Error("expected error for missing UPSTREAM, got nil")
	}
	if err.Error() != "UPSTREAM is required" {
		t.Errorf("expected error 'UPSTREAM is required', got %v", err)
	}
}

// TestLoad_UpstreamParsing tests upstream URL parsing
func TestLoad_UpstreamParsing(t *testing.T) {
	origUpstream := os.Getenv("UPSTREAM")
	origPort := os.Getenv("PORT")
	defer func() {
		os.Setenv("UPSTREAM", origUpstream)
		os.Setenv("PORT", origPort)
	}()

	tests := []struct {
		name           string
		upstream       string
		wantErr        bool
		wantErrContain string
		wantCount      int
		wantFirst      string
	}{
		{
			name:      "single http upstream",
			upstream:  "http://localhost:8080",
			wantErr:   false,
			wantCount: 1,
			wantFirst: "http://localhost:8080",
		},
		{
			name:      "single https upstream",
			upstream:  "https://example.com",
			wantErr:   false,
			wantCount: 1,
			wantFirst: "https://example.com",
		},
		{
			name:      "multiple upstreams",
			upstream:  "http://localhost:8080, https://example.com",
			wantErr:   false,
			wantCount: 2,
			wantFirst: "http://localhost:8080",
		},
		{
			name:           "invalid scheme",
			upstream:       "ftp://example.com",
			wantErr:        true,
			wantErrContain: "scheme must be http or https",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("UPSTREAM", tt.upstream)
			os.Unsetenv("PORT")

			cfg, err := Load()
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
					return
				}
				if tt.wantErrContain != "" && !containsString(err.Error(), tt.wantErrContain) {
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
	envVars := saveEnvVars()
	defer restoreEnvVars(envVars)

	os.Setenv("UPSTREAM", "http://localhost:8080")
	clearOptionalEnv()

	cfg, err := Load()
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
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func saveEnvVars() map[string]string {
	keys := []string{"UPSTREAM", "PORT", "UPSTREAM_TIMEOUT", "DELAY_MIN", "DELAY_MAX", "MAX_WAIT", "ESCALATE_DELAY_AFTER", "ESCALATE_DELAY_MAX_COUNT", "ENDPOINTS"}
	vars := make(map[string]string)
	for _, k := range keys {
		vars[k] = os.Getenv(k)
	}
	return vars
}

func restoreEnvVars(vars map[string]string) {
	for k, v := range vars {
		os.Setenv(k, v)
	}
}

func clearOptionalEnv() {
	keys := []string{"PORT", "UPSTREAM_TIMEOUT", "DELAY_MIN", "DELAY_MAX", "MAX_WAIT", "ESCALATE_DELAY_AFTER", "ESCALATE_DELAY_MAX_COUNT", "ENDPOINTS"}
	for _, k := range keys {
		os.Unsetenv(k)
	}
}
