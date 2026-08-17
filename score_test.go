package meshroute

import "testing"

func TestScoreExpressions(t *testing.T) {
	tests := []struct {
		aggregate Aggregate
		want      float64
	}{{AggregateSum, 9}, {AggregateDiff, -3}, {AggregateMax, 5}, {AggregateMin, 1}, {AggregateAvg, 3}}
	for _, tt := range tests {
		score, ok := (ScoreExpression{Metric: MetricLatency, Aggregate: tt.aggregate}).Evaluate([]float64{3, 1, 5})
		if !ok || score != tt.want {
			t.Errorf("%s score=%v,%v want %v", tt.aggregate, score, ok, tt.want)
		}
	}
}
func TestScoreRejectsEmpty(t *testing.T) {
	if _, ok := (ScoreExpression{Aggregate: AggregateAvg}).Evaluate(nil); ok {
		t.Fatal("empty input accepted")
	}
}
