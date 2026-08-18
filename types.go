package meshroute

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Metric string

const (
	MetricLoss    Metric = "loss"
	MetricLatency Metric = "latency"
)

type Aggregate string

const (
	AggregateSum  Aggregate = "sum"
	AggregateDiff Aggregate = "diff"
	AggregateMax  Aggregate = "max"
	AggregateMin  Aggregate = "min"
	AggregateAvg  Aggregate = "avg"
)

type Selection string

const (
	SelectMin Selection = "min"
	SelectMax Selection = "max"
)

type Candidate struct {
	NodeID string `json:"node_id"`
	IP     net.IP `json:"ip"`
}
type Route struct {
	Domain     string
	Family     int
	Candidates []Candidate
	Source     string
	Target     string
	Matcher    *regexp.Regexp
	Metric     Metric
	Aggregate  Aggregate
	Selection  Selection
	TTL        uint32
	Fallback   net.IP
	Weighted   *WeightedSpec
}

type WeightedSpec struct {
	TargetCIDR   *net.IPNet
	Ports        []uint16
	TargetWeight float64
	PublicWeight float64
}

func (r Route) Key() string {
	if r.Family == 4 {
		return r.Domain + "|ipv4"
	}
	if r.Family == 6 {
		return r.Domain + "|ipv6"
	}
	return r.Domain
}

type Observation struct {
	Values []float64 `json:"values"`
	Error  string    `json:"error,omitempty"`
}
type NodeState struct {
	NodeID       string                 `json:"node_id"`
	IP           string                 `json:"ip"`
	Version      uint64                 `json:"version"`
	UpdatedAt    time.Time              `json:"updated_at"`
	Healthy      bool                   `json:"healthy"`
	PublicIPv4   string                 `json:"public_ipv4,omitempty"`
	PublicIPv6   string                 `json:"public_ipv6,omitempty"`
	Observations map[string]Observation `json:"observations"`
	ReceivedAt   time.Time              `json:"-"`
}
type Snapshot struct {
	Sender string      `json:"sender"`
	States []NodeState `json:"states"`
}

type ScoreExpression struct {
	Metric    Metric
	Aggregate Aggregate
}

func ParseScore(metric, aggregate string) (ScoreExpression, error) {
	m := Metric(metric)
	if m != MetricLoss && m != MetricLatency {
		return ScoreExpression{}, fmt.Errorf("unknown metric %q", metric)
	}
	a := Aggregate(aggregate)
	switch a {
	case AggregateSum, AggregateDiff, AggregateMax, AggregateMin, AggregateAvg:
	default:
		return ScoreExpression{}, fmt.Errorf("unknown aggregate %q", aggregate)
	}
	return ScoreExpression{Metric: m, Aggregate: a}, nil
}
func (s ScoreExpression) Evaluate(values []float64) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	result := values[0]
	switch s.Aggregate {
	case AggregateSum, AggregateAvg:
		for _, v := range values[1:] {
			result += v
		}
		if s.Aggregate == AggregateAvg {
			result /= float64(len(values))
		}
	case AggregateDiff:
		for _, v := range values[1:] {
			result -= v
		}
	case AggregateMax:
		for _, v := range values[1:] {
			if v > result {
				result = v
			}
		}
	case AggregateMin:
		for _, v := range values[1:] {
			if v < result {
				result = v
			}
		}
	}
	return result, true
}

func normalizeDomain(s string) string { return strings.ToLower(strings.TrimSuffix(s, ".")) + "." }
func sortCandidates(c []Candidate) {
	sort.Slice(c, func(i, j int) bool { return c[i].NodeID < c[j].NodeID })
}
