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

type State struct {
	URL *url.URL

	mu              sync.Mutex
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

func (s *State) NextMinTs() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextMinTs
}

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

func (s *State) checkEscalation(rng *rand.Rand) {
	if len(s.window) < s.escalateAfter-1 {
		return
	}

	span := s.window[len(s.window)-1].ts.Sub(s.window[0].ts)
	threshold := time.Duration(int64(s.delayMax) * int64(s.escalateAfter))

	slog.Info("Escalation check", "span", span.Milliseconds(), "threshold", threshold.Milliseconds())
	if span > threshold {
		slog.Info("De-escalating", "escalation", s.escalationCount)
		s.delayMin = s.baseDelayMin
		s.delayMax = s.baseDelayMax
		s.escalationCount = 0
		s.window = nil
		return
	}

	// Only escalate if the oldest request in the window has the same escalation count
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

func randDuration(rng *rand.Rand, min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}
	return min + time.Duration(rng.Int63n(int64(max-min)))
}
