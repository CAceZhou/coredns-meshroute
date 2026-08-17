package meshroute

import (
	"net"
	"testing"
	"time"
)

func TestStoreBestIsDeterministicAndIgnoresStale(t *testing.T) {
	now := time.Now().UTC()
	store := NewMemoryStore()
	store.SetLocal(NodeState{NodeID: "b", Version: 1, Healthy: true, Observations: map[string]Observation{"example.": {Values: []float64{10}}}})
	store.Merge([]NodeState{{NodeID: "a", Version: 1, Healthy: true, Observations: map[string]Observation{"example.": {Values: []float64{10}}}}})
	route := Route{Domain: "example.", Candidates: []Candidate{{"b", net.ParseIP("192.0.2.2")}, {"a", net.ParseIP("192.0.2.1")}}, Metric: MetricLatency, Aggregate: AggregateAvg, Selection: SelectMin}
	best, _, ok := store.Best(route, now, time.Minute)
	if !ok || best.NodeID != "a" {
		t.Fatalf("best=%#v ok=%v, want node a", best, ok)
	}
	store.mu.Lock()
	state := store.states["a"]
	state.ReceivedAt = now.Add(-2 * time.Minute)
	store.states["a"] = state
	store.mu.Unlock()
	best, _, ok = store.Best(route, now, time.Minute)
	if !ok || best.NodeID != "b" {
		t.Fatalf("best=%#v ok=%v, want node b", best, ok)
	}
}

func TestMergeRejectsOldVersions(t *testing.T) {
	store := NewMemoryStore()
	store.Merge([]NodeState{{NodeID: "a", Version: 2, IP: "192.0.2.2"}, {NodeID: "a", Version: 1, IP: "192.0.2.1"}})
	if got := store.Snapshot()[0].IP; got != "192.0.2.2" {
		t.Fatalf("IP=%s", got)
	}
}
