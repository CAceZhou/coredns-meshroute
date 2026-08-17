package meshroute

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

type captureWriter struct{ msg *dns.Msg }

func (*captureWriter) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 53}
}
func (*captureWriter) RemoteAddr() net.Addr {
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.2"), Port: 12345}
}
func (*captureWriter) Write([]byte) (int, error)     { return 0, nil }
func (w *captureWriter) WriteMsg(msg *dns.Msg) error { w.msg = msg.Copy(); return nil }
func (*captureWriter) Close() error                  { return nil }
func (*captureWriter) TsigStatus() error             { return nil }
func (*captureWriter) TsigTimersOnly(bool)           {}
func (*captureWriter) Hijack()                       {}

func TestServeDNSReturnsBestCandidate(t *testing.T) {
	route := Route{Domain: "service.example.", Candidates: []Candidate{{"a", net.ParseIP("192.0.2.1")}, {"b", net.ParseIP("192.0.2.2")}}, Metric: MetricLatency, Aggregate: AggregateAvg, Selection: SelectMin, TTL: 7}
	store := NewMemoryStore()
	store.SetLocal(NodeState{NodeID: "a", Version: 1, Healthy: true, Observations: map[string]Observation{route.Domain: {Values: []float64{20}}}})
	store.Merge([]NodeState{{NodeID: "b", Version: 1, Healthy: true, Observations: map[string]Observation{route.Domain: {Values: []float64{10}}}}})
	handler := &MeshRoute{cfg: Config{Routes: []Route{route}, Timeout: time.Minute}, service: &Service{store: store}}
	request := new(dns.Msg)
	request.SetQuestion("service.example.", dns.TypeA)
	writer := new(captureWriter)
	rcode, err := handler.ServeDNS(context.Background(), writer, request)
	if err != nil || rcode != dns.RcodeSuccess {
		t.Fatalf("rcode=%d err=%v", rcode, err)
	}
	answer, ok := writer.msg.Answer[0].(*dns.A)
	if !ok || !answer.A.Equal(net.ParseIP("192.0.2.2")) || answer.Hdr.Ttl != 7 {
		t.Fatalf("answer=%#v", writer.msg.Answer)
	}
}
