// Package upstream manages upstream server state and escalation.
package upstream

import (
	"log/slog"
	"math/rand"
	"net/url"
	"sync"
	"time"

	"throttle-proxy/internal/config"
)

// requestMeta is one entry of the escalation sliding window.
type requestMeta struct {
	ts time.Time
	// escalationLevel is the escalation count at the time of the request.
	escalationLevel int
}

// State tracks request timing and escalation for one upstream. Exported methods
// are safe for concurrent use.
type State struct {
	// URL is the upstream server address.
	URL *url.URL

	// mu guards every mutable field below.
	mu sync.Mutex

	// nextMinTs is the earliest time the next request may be sent.
	nextMinTs time.Time

	delayMin time.Duration
	delayMax time.Duration

	escalationCount int

	// window holds at most escalateAfter recent requests.
	window []requestMeta

	baseDelayMin time.Duration
	baseDelayMax time.Duration

	// escalateAfter is the window size that triggers an escalation check; 0 disables it.
	escalateAfter int

	// escalateMaxCount caps escalations; 0 means unlimited.
	escalateMaxCount int

	escalateFactorMin float64
	escalateFactorMax float64
}

// NewState returns a State for u driven by cfg. Escalation factors fall back to
// 1.5x-2.0x only when both are zero.
func NewState(u *url.URL, cfg *config.Config) *State {
	factorMin := cfg.EscalateFactorMin
	factorMax := cfg.EscalateFactorMax
	if factorMin == 0 && factorMax == 0 {
		factorMin, factorMax = 1.5, 2.0
	}

	return &State{
		URL:               u,
		nextMinTs:         time.Now(),
		delayMin:          cfg.DelayMin,
		delayMax:          cfg.DelayMax,
		baseDelayMin:      cfg.DelayMin,
		baseDelayMax:      cfg.DelayMax,
		escalateAfter:     cfg.EscalateAfter,
		escalateMaxCount:  cfg.EscalateMaxCount,
		escalateFactorMin: factorMin,
		escalateFactorMax: factorMax,
		window:            []requestMeta{},
	}
}

// NextMinTs returns the earliest time the next request may be sent.
func (s *State) NextMinTs() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextMinTs
}

// UpdateAfterRequest records a request completed at now, reschedules the next send
// time and may escalate or reset the delays.
func (s *State) UpdateAfterRequest(now time.Time, rng *rand.Rand) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.escalateAfter > 0 {
		s.window = append(s.window, requestMeta{ts: now, escalationLevel: s.escalationCount})
		s.window = s.window[max(0, len(s.window)-s.escalateAfter):]
		s.checkEscalation(rng)
	}

	s.nextMinTs = now.Add(randDuration(rng, s.delayMin, s.delayMax))
}

// checkEscalation multiplies both delays up while the recent window of requests fits
// under threshold = delayMax*escalateAfter, and resets them to base once traffic slows.
// Factors are randomized so concurrent proxy instances do not sync their patterns.
func (s *State) checkEscalation(rng *rand.Rand) {
	if len(s.window) < s.escalateAfter {
		return
	}

	span := s.window[len(s.window)-1].ts.Sub(s.window[0].ts)
	threshold := time.Duration(int64(s.delayMax) * int64(s.escalateAfter))

	slog.Info("Escalation check", "span", span.Milliseconds(), "threshold", threshold.Milliseconds())

	// A span below one delay step means the window was still filled under a smaller
	// delay (stale entries right after escalation) and carries no rate signal.
	if span < s.delayMin {
		return
	}

	if span > threshold {
		slog.Info("De-escalating", "escalation", s.escalationCount)
		s.delayMin = s.baseDelayMin
		s.delayMax = s.baseDelayMax
		s.escalationCount = 0
		s.window = nil
		return
	}

	// A window from the previous level means the new delays were never exercised.
	if s.window[0].escalationLevel != s.escalationCount {
		return
	}

	if s.escalateMaxCount > 0 && s.escalationCount >= s.escalateMaxCount {
		return
	}

	factor := s.escalateFactorMin + rng.Float64()*(s.escalateFactorMax-s.escalateFactorMin)
	s.delayMin = time.Duration(float64(s.delayMin) * factor)
	s.delayMax = time.Duration(float64(s.delayMax) * factor)
	s.escalationCount++
	slog.Info("Escalated", "escalation", s.escalationCount, "delayMin", s.delayMin.Milliseconds(), "delayMax", s.delayMax.Milliseconds())
}

func randDuration(rng *rand.Rand, minVal, maxVal time.Duration) time.Duration {
	if maxVal <= minVal {
		return minVal
	}
	return minVal + time.Duration(rng.Int63n(int64(maxVal-minVal)))
}
