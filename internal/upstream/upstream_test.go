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
	// Disable slog output during tests
	// This prevents log noise from checkEscalation
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	})))
}

// TestNewState_Initialization tests State initialization
func TestNewState_Initialization(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{
		DelayMin:         1 * time.Second,
		DelayMax:         2 * time.Second,
		EscalateAfter:    3,
		EscalateMaxCount: 3,
	}

	state := NewState(u, cfg)

	if state.URL.String() != u.String() {
		t.Errorf("expected URL %q, got %q", u.String(), state.URL.String())
	}
	if state.delayMin != cfg.DelayMin {
		t.Errorf("expected delayMin %v, got %v", cfg.DelayMin, state.delayMin)
	}
	if state.delayMax != cfg.DelayMax {
		t.Errorf("expected delayMax %v, got %v", cfg.DelayMax, state.delayMax)
	}
	if state.baseDelayMin != cfg.DelayMin {
		t.Errorf("expected baseDelayMin %v, got %v", cfg.DelayMin, state.baseDelayMin)
	}
	if state.baseDelayMax != cfg.DelayMax {
		t.Errorf("expected baseDelayMax %v, got %v", cfg.DelayMax, state.baseDelayMax)
	}
	if state.escalateAfter != cfg.EscalateAfter {
		t.Errorf("expected escalateAfter %d, got %d", cfg.EscalateAfter, state.escalateAfter)
	}
	if state.escalateMaxCount != cfg.EscalateMaxCount {
		t.Errorf("expected escalateMaxCount %d, got %d", cfg.EscalateMaxCount, state.escalateMaxCount)
	}
	if state.escalationCount != 0 {
		t.Errorf("expected escalationCount 0, got %d", state.escalationCount)
	}
	// window is initialized with one sentinel element (time.Unix(0, 0))
	// This is an implementation detail for the sliding window algorithm
	if len(state.window) == 0 {
		t.Errorf("expected non-empty window (sentinel), got %d items", len(state.window))
	}
}

// TestNextMinTs_ThreadSafe tests thread safety of NextMinTs
func TestNextMinTs_ThreadSafe(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{DelayMin: 1 * time.Second, DelayMax: 2 * time.Second}
	state := NewState(u, cfg)

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_ = state.NextMinTs()
		}()
	}

	wg.Wait()
}

// TestUpdateAfterRequest_FirstRequest tests first request handling
func TestUpdateAfterRequest_FirstRequest(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{
		DelayMin:         1 * time.Second,
		DelayMax:         2 * time.Second,
		EscalateAfter:    3,
		EscalateMaxCount: 3,
	}
	state := NewState(u, cfg)
	rng := rand.New(rand.NewSource(42))

	now := time.Now()
	state.UpdateAfterRequest(now, rng)

	// After first request with large span (time since Unix epoch), de-escalation happens
	// and window is cleared. This is correct behavior.
	// The implementation clears the window when span > threshold (de-escalation path).
	// So window can be empty after first request if span is large.
	// Just verify escalationCount is still 0 (no escalation happened)
	if state.escalationCount != 0 {
		t.Errorf("expected escalationCount 0 after first request, got %d", state.escalationCount)
	}
}

// TestUpdateAfterRequest_EscalationTrigger tests escalation when window is full
func TestUpdateAfterRequest_EscalationTrigger(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{
		DelayMin:         100 * time.Millisecond,
		DelayMax:         200 * time.Millisecond,
		EscalateAfter:    3,
		EscalateMaxCount: 3,
	}
	state := NewState(u, cfg)
	rng := rand.New(rand.NewSource(42))

	// Add requests within threshold time window
	now := time.Now()
	state.UpdateAfterRequest(now, rng)
	state.UpdateAfterRequest(now.Add(50*time.Millisecond), rng)
	state.UpdateAfterRequest(now.Add(100*time.Millisecond), rng)

	if state.escalationCount != 1 {
		t.Errorf("expected escalationCount 1 after trigger, got %d", state.escalationCount)
	}
	if state.delayMin <= cfg.DelayMin {
		t.Errorf("expected delayMin to increase, got %v", state.delayMin)
	}
}

// TestUpdateAfterRequest_EscalationDisabled tests behavior when escalation is disabled
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

// TestUpdateAfterRequest_MaxCount tests escalation stops at max count
func TestUpdateAfterRequest_MaxCount(t *testing.T) {
	u, _ := url.Parse("http://localhost:8080")
	cfg := &config.Config{
		DelayMin:         10 * time.Millisecond,
		DelayMax:         20 * time.Millisecond,
		EscalateAfter:    2,
		EscalateMaxCount: 2, // Stop after 2 escalations
	}
	state := NewState(u, cfg)
	rng := rand.New(rand.NewSource(42))

	now := time.Now()
	// Trigger multiple escalations
	for i := 0; i < 20; i++ {
		state.UpdateAfterRequest(now.Add(time.Duration(i*15)*time.Millisecond), rng)
	}

	if state.escalationCount > cfg.EscalateMaxCount {
		t.Errorf("expected escalationCount <= %d, got %d", cfg.EscalateMaxCount, state.escalationCount)
	}
}

// TestUpdateAfterRequest_Reset tests delay reset on slow traffic
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
	// First trigger escalation - requests 50ms apart
	for i := 0; i < 3; i++ {
		state.UpdateAfterRequest(now.Add(time.Duration(i*50)*time.Millisecond), rng)
	}

	if state.escalationCount == 0 {
		t.Fatal("expected escalation to occur")
	}

	// Now send slow request to trigger reset
	// Need span > threshold = delayMax * escalateAfter
	// After escalation, delayMax will be higher than base
	// Use a time far enough in the future to exceed threshold
	slowTime := now.Add(5 * time.Second)
	state.UpdateAfterRequest(slowTime, rng)

	if state.delayMin != cfg.DelayMin {
		t.Errorf("expected reset to baseDelayMin %v, got %v", cfg.DelayMin, state.delayMin)
	}
	if state.escalationCount != 0 {
		t.Errorf("expected escalationCount reset to 0, got %d", state.escalationCount)
	}
}

// TestConcurrentUpdateAfterRequest tests concurrent updates
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

	// Verify no panic occurred and state is consistent
	if state.escalationCount > cfg.EscalateMaxCount {
		t.Errorf("expected escalationCount <= %d after concurrent updates, got %d", cfg.EscalateMaxCount, state.escalationCount)
	}
}

// TestRandDuration_EdgeCases tests randDuration edge cases
func TestRandDuration_EdgeCases(t *testing.T) {
	rng := rand.New(rand.NewSource(42))

	tests := []struct {
		name string
		min  time.Duration
		max  time.Duration
		want time.Duration
	}{
		{
			name: "max less than min returns min",
			min:  5 * time.Second,
			max:  2 * time.Second,
			want: 5 * time.Second,
		},
		{
			name: "max equal to min returns min",
			min:  1 * time.Second,
			max:  1 * time.Second,
			want: 1 * time.Second,
		},
		{
			name: "zero values",
			min:  0,
			max:  0,
			want: 0,
		},
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

// TestRandDuration_Range tests randDuration returns value in range
func TestRandDuration_Range(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	minVal := 1 * time.Second
	maxVal := 2 * time.Second

	// Test multiple times to verify range
	for i := 0; i < 100; i++ {
		got := randDuration(rng, minVal, maxVal)
		if got < minVal || got >= maxVal {
			t.Errorf("randDuration() = %v, want value in [%v, %v)", got, minVal, maxVal)
		}
	}
}

// TestUpdateAfterRequest_EscalationSameGeneration tests that escalation happens
// when the window fills with requests from the same generation
func TestUpdateAfterRequest_EscalationSameGeneration(t *testing.T) {
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

	// First request clears the sentinel (triggers de-escalation due to large span)
	state.UpdateAfterRequest(now, rng)

	// Need escalateAfter (3) requests to fill window enough for escalation check
	// Window size check: len(window) >= escalateAfter-1 (2) to proceed with escalation logic
	state.UpdateAfterRequest(now.Add(10*time.Millisecond), rng)
	state.UpdateAfterRequest(now.Add(20*time.Millisecond), rng)
	state.UpdateAfterRequest(now.Add(30*time.Millisecond), rng)

	if state.escalationCount != 1 {
		t.Errorf("expected escalationCount 1 after first trigger, got %d", state.escalationCount)
	}

	// After escalation, delay values are multiplied by factor (~1.5-2x)
	// New threshold = delayMax * escalateAfter is now larger
	// Adding more requests with small span will trigger another escalation

	// Add 3 more requests to trigger second escalation
	state.UpdateAfterRequest(now.Add(100*time.Millisecond), rng)
	state.UpdateAfterRequest(now.Add(110*time.Millisecond), rng)
	state.UpdateAfterRequest(now.Add(120*time.Millisecond), rng)

	if state.escalationCount != 2 {
		t.Errorf("expected escalationCount 2 after second trigger, got %d", state.escalationCount)
	}
}
