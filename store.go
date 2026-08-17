package meshroute

import (
	"sort"
	"sync"
	"time"
)

type StateStore interface {
	Merge([]NodeState)
	SetLocal(NodeState)
	Snapshot() []NodeState
	Best(Route, time.Time, time.Duration) (Candidate, float64, bool)
}

type MemoryStore struct {
	mu     sync.RWMutex
	states map[string]NodeState
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{states: make(map[string]NodeState)} }
func (s *MemoryStore) SetLocal(state NodeState) {
	state.ReceivedAt = time.Now().UTC()
	s.mu.Lock()
	s.states[state.NodeID] = copyState(state)
	s.mu.Unlock()
}
func (s *MemoryStore) Merge(states []NodeState) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, incoming := range states {
		current, ok := s.states[incoming.NodeID]
		if ok && incoming.Version <= current.Version {
			continue
		}
		incoming.ReceivedAt = now
		s.states[incoming.NodeID] = copyState(incoming)
	}
}
func (s *MemoryStore) Snapshot() []NodeState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]NodeState, 0, len(s.states))
	for _, st := range s.states {
		out = append(out, copyState(st))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}
func (s *MemoryStore) Best(route Route, now time.Time, timeout time.Duration) (Candidate, float64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	expr := ScoreExpression{Metric: route.Metric, Aggregate: route.Aggregate}
	candidates := append([]Candidate(nil), route.Candidates...)
	sortCandidates(candidates)
	var best Candidate
	var bestScore float64
	found := false
	for _, candidate := range candidates {
		st, ok := s.states[candidate.NodeID]
		if !ok || !st.Healthy || now.Sub(st.ReceivedAt) > timeout {
			continue
		}
		obs, ok := st.Observations[route.Key()]
		if !ok || obs.Error != "" {
			continue
		}
		score, ok := expr.Evaluate(obs.Values)
		if !ok {
			continue
		}
		if !found || better(score, bestScore, route.Selection) {
			best, bestScore, found = candidate, score, true
		}
	}
	return best, bestScore, found
}
func better(a, b float64, selection Selection) bool {
	if selection == SelectMax {
		return a > b
	}
	return a < b
}
func copyState(in NodeState) NodeState {
	out := in
	out.Observations = make(map[string]Observation, len(in.Observations))
	for k, v := range in.Observations {
		v.Values = append([]float64(nil), v.Values...)
		out.Observations[k] = v
	}
	return out
}
