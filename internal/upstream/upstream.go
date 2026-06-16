// Package upstream tracks per-upstream request timing and escalation.
package upstream

import (
	"log/slog"
	"math/rand"
	"net/url"
	"sync"
	"time"

	"throttle-proxy/internal/config"
)

type requestMeta struct {
	ts              time.Time
	escalationLevel int
}

// State tracks per-upstream request timing and delay escalation.
type State struct {
	URL *url.URL

	mu sync.Mutex

	nextMinTs       time.Time
	delayMin        time.Duration
	delayMax        time.Duration
	escalationCount int
	window          []requestMeta

	baseDelayMin      time.Duration
	baseDelayMax      time.Duration
	escalateAfter     int
	escalateMaxCount  int
	escalateFactorMin float64
	escalateFactorMax float64
}

// NewState returns a new State for the given upstream URL and configuration.
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

// UpdateAfterRequest updates state after a request is completed and returns
// the new earliest allowed fire time.
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

// checkEscalation adjusts delays based on the recent request window.
func (s *State) checkEscalation(rng *rand.Rand) {
	// Skip if window doesn't have enough entries for a meaningful check.
	if len(s.window) < s.escalateAfter {
		return
	}

	// Calculate time span from oldest to newest request in the window.
	span := s.window[len(s.window)-1].ts.Sub(s.window[0].ts)
	// Threshold: expected time for escalateAfter requests at current delayMax.
	threshold := time.Duration(int64(s.delayMax) * int64(s.escalateAfter))

	slog.Info("Escalation check", "span", span.Milliseconds(), "threshold", threshold.Milliseconds())

	if span < s.delayMin {
		return
	}

	// De-escalation: Requests are coming slowly enough, reset to base delays.
	if span > threshold {
		slog.Info("De-escalating", "escalation", s.escalationCount)
		s.delayMin = s.baseDelayMin
		s.delayMax = s.baseDelayMax
		s.escalationCount = 0
		s.window = nil
		return
	}

	// Wait for a full window at the current escalation level before escalating again.
	if s.window[0].escalationLevel != s.escalationCount {
		return
	}

	if s.escalateMaxCount > 0 && s.escalationCount >= s.escalateMaxCount {
		return
	}

	// Randomize factor to avoid synchronized delays across instances.
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
