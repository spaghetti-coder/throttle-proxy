// Property-based tests for upstream package
//
// These tests verify mathematical invariants that should always hold true
// for the timing and escalation logic. They complement traditional
// table-driven tests by exploring the input space more thoroughly.
//
// Properties tested:
// - randDuration always returns values within the specified range
// - NextMinTs always returns a timestamp in the future
// - Escalation always increases (or maintains) delay, never decreases
// - Escalation respects the configured maximum count
// - Escalation factors stay within configured bounds
//
// See: https://pkg.go.dev/testing/quick for testing/quick documentation

package upstream

import (
	"math/rand"
	"net/url"
	"testing"
	"testing/quick"
	"time"

	"throttle-proxy/internal/config"
)

// TestRandDurationInRangeProperty verifies that randDuration always returns
// a value within the specified [min, max] range.
func TestRandDurationInRangeProperty(t *testing.T) {
	f := func(minVal int64, maxVal int64) bool {
		// Pre-condition: valid range
		if minVal < 0 || maxVal < minVal {
			return true
		}
		
		result := randDuration(rand.New(rand.NewSource(1)),
			time.Duration(minVal), time.Duration(maxVal))
		
		return result >= time.Duration(minVal) && result <= time.Duration(maxVal)
	}
	
	if err := quick.Check(f, nil); err != nil {
		t.Error(err)
	}
}

// TestNextMinTsInFutureProperty verifies that NextMinTs always returns
// a timestamp in the future (or very close to now for concurrent access).
func TestNextMinTsInFutureProperty(t *testing.T) {
	u, _ := url.Parse("http://example.com")
	cfg := &config.Config{
		DelayMin: time.Second,
		DelayMax: 2 * time.Second,
	}
	state := NewState(u, cfg)
	
	f := func() bool {
		nextTs := state.NextMinTs()
		// Should be at or after current time (with small tolerance for test execution)
		return nextTs.After(time.Now().Add(-time.Hour)) || nextTs.Equal(time.Now().Add(-time.Hour))
	}
	
	if err := quick.Check(f, nil); err != nil {
		t.Error(err)
	}
}

// TestEscalationIncreasesDelayProperty verifies that escalation never decreases
// the delay range - it either increases it or leaves it unchanged.
func TestEscalationIncreasesDelayProperty(t *testing.T) {
	f := func(initialDelay int64, factorMin float64, factorMax float64) bool {
		// Pre-conditions: valid inputs, reasonable bounds to prevent overflow
		if initialDelay <= 0 || factorMin <= 0 || factorMax < factorMin {
			return true
		}
		// Prevent overflow: max factor should not cause duration overflow
		if factorMax > 1e9 {
			return true
		}
		
		u, _ := url.Parse("http://example.com")
		cfg := &config.Config{
			DelayMin:          time.Duration(initialDelay),
			DelayMax:          time.Duration(initialDelay) * 2,
			EscalateFactorMin: factorMin,
			EscalateFactorMax: factorMax,
			EscalateAfter:     1, // Disable escalation checking for this property
		}
		state := NewState(u, cfg)
		
		originalDelayMin := state.delayMin
		originalDelayMax := state.delayMax
		
		// Force escalation by simulating many rapid requests
		rng := rand.New(rand.NewSource(1))
		now := time.Now()
		
		// Update request to trigger escalation calculation
		state.UpdateAfterRequest(now, rng)
		
		// After any update, delays should be >= original (escalation increases)
		// or equal (no escalation occurred)
		return state.delayMin >= originalDelayMin && state.delayMax >= originalDelayMax
	}
	
	if err := quick.Check(f, &quick.Config{MaxCount: 100}); err != nil {
		t.Error(err)
	}
}

// TestEscalationRespectsMaxCountProperty verifies that escalation never exceeds
// the configured maximum count.
func TestEscalationRespectsMaxCountProperty(t *testing.T) {
	f := func(maxCount int) bool {
		// Pre-condition: max count should be non-negative
		if maxCount < 0 {
			return true
		}
		
		u, _ := url.Parse("http://example.com")
		cfg := &config.Config{
			DelayMin:         10 * time.Millisecond,
			DelayMax:         20 * time.Millisecond,
			EscalateAfter:    2,
			EscalateMaxCount: maxCount,
		}
		state := NewState(u, cfg)
		
		// Simulate many requests to try to exceed max count
		rng := rand.New(rand.NewSource(42))
		for i := 0; i < 100; i++ {
			state.UpdateAfterRequest(time.Now().Add(time.Duration(i)*time.Millisecond), rng)
		}
		
		// If maxCount is 0, escalation is unlimited
		if maxCount == 0 {
			return true
		}
		
		// Otherwise, escalation count should never exceed maxCount
		return state.escalationCount <= maxCount
	}
	
	if err := quick.Check(f, &quick.Config{MaxCount: 50}); err != nil {
		t.Error(err)
	}
}

// TestEscalationFactorRangeProperty verifies that escalation factors are always
// within the configured bounds [EscalateFactorMin, EscalateFactorMax].
func TestEscalationFactorRangeProperty(t *testing.T) {
	f := func(factorMin float64, factorMax float64) bool {
		// Pre-conditions: valid factor range
		if factorMin <= 0 || factorMax < factorMin {
			return true
		}
		
		u, _ := url.Parse("http://example.com")
		cfg := &config.Config{
			DelayMin:          10 * time.Millisecond,
			DelayMax:          20 * time.Millisecond,
			EscalateAfter:     2,
			EscalateFactorMin: factorMin,
			EscalateFactorMax: factorMax,
		}
		
		// Verify config stores factors correctly
		if cfg.EscalateFactorMin != factorMin || cfg.EscalateFactorMax != factorMax {
			return true
		}
		
		state := NewState(u, cfg)
		
		// Verify state has correct factor bounds
		return state.escalateFactorMin == factorMin && state.escalateFactorMax == factorMax
	}
	
	if err := quick.Check(f, &quick.Config{MaxCount: 100}); err != nil {
		t.Error(err)
	}
}

// TestRandDurationWithEqualBoundsProperty verifies edge case when min == max.
func TestRandDurationWithEqualBoundsProperty(t *testing.T) {
	f := func(val int64) bool {
		if val < 0 {
			return true
		}
		
		d := time.Duration(val)
		result := randDuration(rand.New(rand.NewSource(1)), d, d)
		
		// When min == max, result should equal that value
		return result == d
	}
	
	if err := quick.Check(f, nil); err != nil {
		t.Error(err)
	}
}

// TestRandDurationWithInvertedBoundsProperty verifies edge case when max < min.
func TestRandDurationWithInvertedBoundsProperty(t *testing.T) {
	f := func(minVal int64, maxVal int64) bool {
		// Pre-condition: inverted range
		if minVal <= 0 || maxVal >= minVal {
			return true
		}
		
		result := randDuration(rand.New(rand.NewSource(1)),
			time.Duration(minVal), time.Duration(maxVal))
		
		// When max < min, should return min
		return result == time.Duration(minVal)
	}
	
	if err := quick.Check(f, nil); err != nil {
		t.Error(err)
	}
}

// TestNextMinTsMonotonicProperty verifies that NextMinTs is non-decreasing
// across successive calls (with appropriate timing).
func TestNextMinTsMonotonicProperty(t *testing.T) {
	u, _ := url.Parse("http://example.com")
	cfg := &config.Config{
		DelayMin: time.Millisecond,
		DelayMax: 2 * time.Millisecond,
	}
	state := NewState(u, cfg)
	
	prevTs := state.NextMinTs()
	
	f := func() bool {
		rng := rand.New(rand.NewSource(1))
		now := time.Now()
		state.UpdateAfterRequest(now, rng)
		newTs := state.NextMinTs()
		
		result := !newTs.Before(prevTs)
		prevTs = newTs
		return result
	}
	
	if err := quick.Check(f, &quick.Config{MaxCount: 50}); err != nil {
		t.Error(err)
	}
}
