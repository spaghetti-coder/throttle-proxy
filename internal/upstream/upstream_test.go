package upstream

import (
	"log/slog"
	"math/rand"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"throttle-proxy/internal/config"
)

func init() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	})))
}

func TestNewState_Initialization(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{
		DelayMin:         1 * time.Second,
		DelayMax:         2 * time.Second,
		EscalateAfter:    3,
		EscalateMaxCount: 3,
	}

	state := NewState(u, cfg)

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"URL", state.URL.String(), u.String()},
		{"delayMin", state.delayMin, cfg.DelayMin},
		{"delayMax", state.delayMax, cfg.DelayMax},
		{"baseDelayMin", state.baseDelayMin, cfg.DelayMin},
		{"baseDelayMax", state.baseDelayMax, cfg.DelayMax},
		{"escalateAfter", state.escalateAfter, cfg.EscalateAfter},
		{"escalateMaxCount", state.escalateMaxCount, cfg.EscalateMaxCount},
		{"escalationCount", state.escalationCount, 0},
		{"window length", len(state.window), 0},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if c.got != c.want {
				t.Errorf("got %v, want %v", c.got, c.want)
			}
		})
	}
}

func TestNewState_NextMinTsInitializedNearNow(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{DelayMin: time.Second, DelayMax: 2 * time.Second}

	before := time.Now()
	state := NewState(u, cfg)
	after := time.Now()

	got := state.NextMinTs()
	if got.Before(before) || got.After(after) {
		t.Errorf("NextMinTs() = %v, want in [%v, %v]", got, before, after)
	}
}

func TestNewState_EscalateFactors(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")

	tests := []struct {
		name    string
		cfgMin  float64
		cfgMax  float64
		wantMin float64
		wantMax float64
	}{
		{"configured values preserved", 2.5, 3.5, 2.5, 3.5},
		{"zero values use defaults", 0, 0, 1.5, 2.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := NewState(u, &config.Config{
				DelayMin:          time.Second,
				DelayMax:          2 * time.Second,
				EscalateFactorMin: tt.cfgMin,
				EscalateFactorMax: tt.cfgMax,
			})
			if state.escalateFactorMin != tt.wantMin || state.escalateFactorMax != tt.wantMax {
				t.Errorf("factors = (%v, %v), want (%v, %v)",
					state.escalateFactorMin, state.escalateFactorMax, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestUpdateAfterRequest_NextMinTsAdvances(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{DelayMin: time.Millisecond, DelayMax: 2 * time.Millisecond}
	state := NewState(u, cfg)
	rng := rand.New(rand.NewSource(1))

	base := time.Now()
	prev := state.NextMinTs()
	for i := 1; i <= 10; i++ {
		state.UpdateAfterRequest(base.Add(time.Duration(i)*time.Second), rng)
		next := state.NextMinTs()
		if next.Before(prev) {
			t.Fatalf("iteration %d: NextMinTs went backwards: prev=%v next=%v", i, prev, next)
		}
		prev = next
	}
}

func TestUpdateAfterRequest_EscalationDisabled(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{
		DelayMin:         1 * time.Second,
		DelayMax:         2 * time.Second,
		EscalateAfter:    0, // Disabled
		EscalateMaxCount: 3,
	}
	state := NewState(u, cfg)
	rng := rand.New(rand.NewSource(42))

	now := time.Now()
	for i := 0; i < 10; i++ {
		state.UpdateAfterRequest(now.Add(time.Duration(i)*time.Second), rng)
	}

	if state.escalationCount != 0 {
		t.Errorf("expected escalationCount 0 when disabled, got %d", state.escalationCount)
	}
	if state.delayMin != cfg.DelayMin {
		t.Errorf("expected delayMin unchanged, got %v", state.delayMin)
	}
}

func TestUpdateAfterRequest_MaxCount(t *testing.T) {
	// Use a deterministic factor so we can guarantee repeated escalations.
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{
		DelayMin:          10 * time.Millisecond,
		DelayMax:          10 * time.Millisecond,
		EscalateAfter:     2,
		EscalateMaxCount:  2,
		EscalateFactorMin: 2.0,
		EscalateFactorMax: 2.0,
	}
	state := NewState(u, cfg)
	rng := rand.New(rand.NewSource(42))

	now := time.Now()
	// Trigger first escalation: span 10ms >= delayMin 10ms and <= delayMax*2 20ms.
	state.UpdateAfterRequest(now, rng)
	state.UpdateAfterRequest(now.Add(10*time.Millisecond), rng)
	if state.escalationCount != 1 {
		t.Fatalf("expected escalationCount 1 after first trigger, got %d", state.escalationCount)
	}

	// Trigger second escalation at the new escalation level.
	// New delays are 20ms; keep span within [20ms, 40ms].
	state.UpdateAfterRequest(now.Add(35*time.Millisecond), rng)
	state.UpdateAfterRequest(now.Add(75*time.Millisecond), rng)

	if state.escalationCount != 2 {
		t.Fatalf("expected escalationCount 2 after second trigger, got %d", state.escalationCount)
	}

	// Try to trigger a third escalation: must stay capped.
	// After second escalation delayMin=delayMax=40ms, so span must be >=40ms
	// to even attempt escalation, but <=40ms*2 to avoid de-escalation reset.
	state.UpdateAfterRequest(now.Add(115*time.Millisecond), rng)
	state.UpdateAfterRequest(now.Add(160*time.Millisecond), rng)

	if state.escalationCount != cfg.EscalateMaxCount {
		t.Errorf("expected escalationCount capped at %d, got %d", cfg.EscalateMaxCount, state.escalationCount)
	}
	if state.delayMin != cfg.DelayMin*4 || state.delayMax != cfg.DelayMax*4 {
		t.Errorf("expected delays doubled twice, got delayMin=%v delayMax=%v", state.delayMin, state.delayMax)
	}
}

func TestUpdateAfterRequest_Reset(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{
		DelayMin:         100 * time.Millisecond,
		DelayMax:         200 * time.Millisecond,
		EscalateAfter:    3,
		EscalateMaxCount: 3,
	}
	state := NewState(u, cfg)
	rng := rand.New(rand.NewSource(42))

	now := time.Now()
	for i := 0; i < 3; i++ {
		state.UpdateAfterRequest(now.Add(time.Duration(i*50)*time.Millisecond), rng)
	}

	if state.escalationCount == 0 {
		t.Fatal("expected escalation to occur")
	}
	slowTime := now.Add(5 * time.Second)
	state.UpdateAfterRequest(slowTime, rng)

	if state.delayMin != cfg.DelayMin {
		t.Errorf("expected reset to baseDelayMin %v, got %v", cfg.DelayMin, state.delayMin)
	}
	if state.escalationCount != 0 {
		t.Errorf("expected escalationCount reset to 0, got %d", state.escalationCount)
	}
}

func TestConcurrentUpdateAfterRequest(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{
		DelayMin:         1 * time.Millisecond,
		DelayMax:         2 * time.Millisecond,
		EscalateAfter:    10,
		EscalateMaxCount: 3,
	}
	state := NewState(u, cfg)
	rng := rand.New(rand.NewSource(42))

	const goroutines = 50
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	now := time.Now()
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				state.UpdateAfterRequest(now.Add(time.Duration(id*iterations+j)*time.Millisecond), rng)
			}
		}(i)
	}

	wg.Wait()
	// Concurrent safety: escalationCount should not exceed max.
	if state.escalationCount > cfg.EscalateMaxCount {
		t.Errorf("expected escalationCount <= %d after concurrent updates, got %d", cfg.EscalateMaxCount, state.escalationCount)
	}
	// State should not be corrupted: window within bounds.
	state.mu.Lock()
	if len(state.window) > cfg.EscalateAfter {
		t.Errorf("expected window size <= %d, got %d", cfg.EscalateAfter, len(state.window))
	}
	state.mu.Unlock()
}
func TestRandDuration_EdgeCases(t *testing.T) {
	rng := rand.New(rand.NewSource(42))

	tests := []struct {
		name string
		min  time.Duration
		max  time.Duration
		want time.Duration
	}{
		{name: "max less than min returns min", min: 5 * time.Second, max: 2 * time.Second, want: 5 * time.Second},
		{name: "max equal to min returns min", min: 1 * time.Second, max: 1 * time.Second, want: 1 * time.Second},
		{name: "zero values", min: 0, max: 0, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := randDuration(rng, tt.min, tt.max)
			if got != tt.want {
				t.Errorf("randDuration() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUpdateAfterRequest_EscalationChains(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{
		DelayMin:         100 * time.Millisecond,
		DelayMax:         200 * time.Millisecond,
		EscalateAfter:    3,
		EscalateMaxCount: 5,
	}
	state := NewState(u, cfg)
	rng := rand.New(rand.NewSource(42))

	now := time.Now()
	state.UpdateAfterRequest(now, rng)
	state.UpdateAfterRequest(now.Add(50*time.Millisecond), rng)
	state.UpdateAfterRequest(now.Add(100*time.Millisecond), rng)
	state.UpdateAfterRequest(now.Add(500*time.Millisecond), rng)
	state.UpdateAfterRequest(now.Add(600*time.Millisecond), rng)
	state.UpdateAfterRequest(now.Add(700*time.Millisecond), rng)

	if state.escalationCount != 2 {
		t.Errorf("expected escalationCount 2 after chain, got %d", state.escalationCount)
	}
}
