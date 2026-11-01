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

// TestEscalationRespectsMaxCountProperty verifies that escalation never exceeds
// the configured maximum count.
func TestEscalationRespectsMaxCountProperty(t *testing.T) {
	f := func(maxCount int) bool {
		if maxCount < 0 {
			return true
		}

		u, _ := url.Parse("http://example.com")
		cfg := &config.Config{
			DelayMin:         1 * time.Millisecond,
			DelayMax:         2 * time.Millisecond,
			EscalateAfter:    2,
			EscalateMaxCount: maxCount,
		}
		state := NewState(u, cfg)

		rng := rand.New(rand.NewSource(42))
		baseTime := time.Now()
		for i := 0; i < 100; i++ {
			state.UpdateAfterRequest(baseTime.Add(time.Duration(i)*2*time.Millisecond), rng)
		}

		if maxCount == 0 {
			return true
		}

		return state.escalationCount <= maxCount
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 50}); err != nil {
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
