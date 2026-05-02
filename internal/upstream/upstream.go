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

// requestMeta tracks metadata for a single request in the sliding window.
// Used by the escalation algorithm to detect patterns in request timing.
type requestMeta struct {
	// ts is the timestamp when the request was processed.
	ts time.Time
	// escalationLevel is the escalation count at the time of the request.
	// Used to ensure escalation only happens when all requests in the window
	// are at the same escalation level, preventing rapid successive escalations.
	escalationLevel int
}

// State tracks request timing and escalation for an upstream server.
// It maintains a sliding window of recent requests to detect when requests
// are coming in faster than the configured delay threshold, triggering escalation
// to increase delays multiplicatively.
type State struct {
	// URL is the upstream server address.
	URL *url.URL

	// mu protects all mutable state fields (nextMinTs, delayMin, delayMax, etc.).
	// Must be held when accessing any field below.
	mu sync.Mutex

	// nextMinTs is the earliest time the next request can be sent.
	// Requests arriving before this time must wait.
	nextMinTs time.Time

	// delayMin is the current minimum delay between requests.
	// This value escalates multiplicatively when requests come too fast.
	delayMin time.Duration

	// delayMax is the current maximum delay between requests.
	// Escalates along with delayMin to maintain the delay range.
	delayMax time.Duration

	// escalationCount tracks how many times delays have been escalated.
	// Used to limit escalation via escalateMaxCount.
	escalationCount int

	// window is a sliding window of recent requests for escalation detection.
	// Contains metadata about the most recent requests up to escalateAfter.
	// Used to calculate the time span between the oldest and newest requests.
	window []requestMeta

	// baseDelayMin is the configured minimum delay (from config).
	// Used to reset delays during de-escalation.
	baseDelayMin time.Duration

	// baseDelayMax is the configured maximum delay (from config).
	// Used to reset delays during de-escalation.
	baseDelayMax time.Duration

	// escalateAfter is the number of requests in the window that triggers
	// escalation checking. When window reaches this size, we check if requests
	// are coming too fast and should trigger escalation.
	escalateAfter int

	// escalateMaxCount is the maximum number of escalations allowed.
	// Set to 0 for unlimited escalation. Prevents delays from growing indefinitely.
	escalateMaxCount int

	// escalateFactorMin is the minimum factor for random escalation multiplier.
	// A random factor between escalateFactorMin and escalateFactorMax is chosen
	// each time escalation occurs.
	escalateFactorMin float64

	// escalateFactorMax is the maximum factor for random escalation multiplier.
	escalateFactorMax float64
}

// NewState creates a new State for the given upstream URL and configuration.
// Initializes the request timing window and sets default escalation factors
// if not configured (1.5x to 2.0x multiplier range).
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
		window:            []requestMeta{{ts: time.Unix(0, 0), escalationLevel: 0}},
	}
}

// NextMinTs returns the earliest time the next request can be sent.
//
// Thread-safe: acquires the state mutex to read nextMinTs.
// Returns the timestamp before which requests must wait.
// Used by the dispatcher to determine when an upstream is ready for the next request.
func (s *State) NextMinTs() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextMinTs
}

// UpdateAfterRequest updates the state after a request is completed.
//
// Complete flow:
//  1. Window management: Add the current request to the sliding window,
//     trimming it to keep only the most recent escalateAfter entries.
//  2. Escalation checking: If the window is full enough, check if requests
//     are coming faster than the configured threshold and escalate if needed.
//  3. Next timestamp calculation: Compute the next minimum timestamp by
//     adding a random delay (between delayMin and delayMax) to the current time.
//
// If escalation is disabled (escalateAfter == 0), only step 3 is performed.
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

// checkEscalation determines if delays should be escalated or de-escalated
// based on the current request window.
//
// Escalation Algorithm:
//
// Escalation increases delay multiplicatively when requests come faster than
// the configured threshold. This prevents overwhelming the upstream server
// during high-traffic periods by backing off progressively.
//
// De-escalation resets delays to base values when request rate slows, restoring
// normal operation once traffic subsides.
//
// Algorithm steps:
//
//  1. Window size check: Skip if window doesn't have enough entries yet.
//     Need at least escalateAfter-1 entries before checking.
//
//  2. Span calculation: Compute the time between the oldest and newest
//     requests in the window. This represents the recent request rate.
//     span = window[last].ts - window[0].ts
//
//  3. Threshold calculation: Compute the expected minimum span based on
//     current delays. threshold = delayMax * escalateAfter
//     This is the minimum time we expect escalateAfter requests to take.
//
//  4. De-escalation path: If span > threshold, requests are coming slowly
//     enough. Reset all delays to base values and clear the window.
//
//  5. Escalation path: If span <= threshold, requests are coming too fast.
//     Check additional conditions before escalating:
//     - Window consistency: Only escalate if the oldest request in the window
//     has the same escalationLevel as the current escalationCount. This
//     prevents multiple rapid escalations from a single burst of requests.
//     - Max escalation: Respect escalateMaxCount limit if set (> 0).
//
//  6. Escalation calculation: Choose a random factor between escalateFactorMin
//     and escalateFactorMax (line 109), then multiply both delayMin and delayMax
//     by this factor. The randomization prevents synchronized delay patterns
//     across multiple proxy instances.
//
// 7. Log the escalation for observability.
func (s *State) checkEscalation(rng *rand.Rand) {
	// Skip if window doesn't have enough entries for a meaningful check.
	if len(s.window) < s.escalateAfter-1 {
		return
	}

	// Calculate time span from oldest to newest request in the window.
	span := s.window[len(s.window)-1].ts.Sub(s.window[0].ts)
	// Threshold: expected time for escalateAfter requests at current delayMax.
	threshold := time.Duration(int64(s.delayMax) * int64(s.escalateAfter))

	slog.Info("Escalation check", "span", span.Milliseconds(), "threshold", threshold.Milliseconds())

	// De-escalation: Requests are coming slowly enough, reset to base delays.
	if span > threshold {
		slog.Info("De-escalating", "escalation", s.escalationCount)
		s.delayMin = s.baseDelayMin
		s.delayMax = s.baseDelayMax
		s.escalationCount = 0
		s.window = nil
		return
	}

	// Escalation condition: Only escalate if the oldest request in the window
	// has the same escalation level as the current count. This ensures we've
	// processed a full window of requests at the current escalation level
	// before escalating again, preventing rapid successive escalations.
	if s.window[0].escalationLevel != s.escalationCount {
		return
	}

	// Respect maximum escalation limit if configured.
	if s.escalateMaxCount > 0 && s.escalationCount >= s.escalateMaxCount {
		return
	}

	// Choose random escalation factor to prevent synchronized patterns.
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
