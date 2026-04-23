package upstream

import (
	"math/rand"
	"net/url"
	"sync"
	"time"

	"throttle-proxy/internal/config"
)

type requestMeta struct {
	ts    time.Time
	count int
}

type State struct {
	URL *url.URL

	mu              sync.Mutex
	nextMinTs       time.Time
	currentDelayMin time.Duration
	currentDelayMax time.Duration
	escalationCount int
	window          []requestMeta

	baseDelayMin     time.Duration
	baseDelayMax     time.Duration
	escalateAfter    int
	escalateMaxCount int
}

func NewState(u *url.URL, cfg *config.Config) *State {
	return &State{
		URL:              u,
		nextMinTs:        time.Now(),
		currentDelayMin:  cfg.DelayMin,
		currentDelayMax:  cfg.DelayMax,
		baseDelayMin:     cfg.DelayMin,
		baseDelayMax:     cfg.DelayMax,
		escalateAfter:    cfg.EscalateAfter,
		escalateMaxCount: cfg.EscalateMaxCount,
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
		s.window = append(s.window, requestMeta{ts: now, count: s.escalationCount})
		if len(s.window) > s.escalateAfter {
			s.window = s.window[1:]
		}
		s.checkEscalation(rng)
	}

	s.nextMinTs = now.Add(randDuration(rng, s.currentDelayMin, s.currentDelayMax))
}

func (s *State) checkEscalation(rng *rand.Rand) {
	if len(s.window) < s.escalateAfter-1 {
		return
	}

	span := s.window[len(s.window)-1].ts.Sub(s.window[0].ts)
	threshold := time.Duration(int64(s.currentDelayMax) * int64(s.escalateAfter))

	if span > threshold {
		// De-escalation - reset to base delays
		s.currentDelayMin = s.baseDelayMin
		s.currentDelayMax = s.baseDelayMax
		s.escalationCount = 0
		s.window = nil
		return
	}

	// Only escalate if the oldest request in the window has the same escalation count
	if s.window[0].count != s.escalationCount {
		return
	}

	if s.escalateMaxCount > 0 && s.escalationCount >= s.escalateMaxCount {
		return
	}

	// Escalation
	oldMin := s.currentDelayMin
	factor := 1.5 + rng.Float64()*0.5
	s.currentDelayMin = time.Duration(float64(oldMin) * factor)
	s.currentDelayMax = s.currentDelayMax + s.currentDelayMin - oldMin
	s.escalationCount++
}

func randDuration(rng *rand.Rand, min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}
	return min + time.Duration(rng.Int63n(int64(max-min)))
}
