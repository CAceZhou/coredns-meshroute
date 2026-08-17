package meshroute

import (
	"context"
	"net"
	"testing"
)

type metricsFunc func(ConnectionFilter) ([]TCPConnection, error)

func (f metricsFunc) Connections(_ context.Context, filter ConnectionFilter) ([]TCPConnection, error) {
	return f(filter)
}

func TestWeightedObservationEqualizesPortsAndWeightsGroups(t *testing.T) {
	_, targetCIDR, _ := net.ParseCIDR("10.0.0.0/16")
	route := Route{Family: 4, Weighted: &WeightedSpec{TargetCIDR: targetCIDR, Ports: []uint16{25565, 25566}, TargetWeight: .6, PublicWeight: .4}}
	metrics := metricsFunc(func(filter ConnectionFilter) ([]TCPConnection, error) {
		if filter.RemoteCIDR != "" {
			return []TCPConnection{{RemotePort: 25565, LossRate: .1}, {RemotePort: 25565, LossRate: .3}, {RemotePort: 25566, LossRate: .4}}, nil
		}
		return []TCPConnection{{LocalPort: 25565, RemoteAddress: "8.8.8.8", LossRate: .2}, {LocalPort: 25565, RemoteAddress: "10.1.1.1", LossRate: 0}, {LocalPort: 25566, RemoteAddress: "1.1.1.1", LossRate: .6}}, nil
	})
	got := (&Service{metrics: metrics}).observeWeighted(context.Background(), route)
	if got.Error != "" || len(got.Values) != 1 || got.Values[0] < .339999 || got.Values[0] > .340001 {
		t.Fatalf("observation=%#v, want .34", got)
	}
}

func TestWeightedObservationReweightsMissingGroup(t *testing.T) {
	_, targetCIDR, _ := net.ParseCIDR("10.0.0.0/16")
	route := Route{Family: 6, Weighted: &WeightedSpec{TargetCIDR: targetCIDR, Ports: []uint16{25565, 25566}, TargetWeight: .6, PublicWeight: .4}}
	metrics := metricsFunc(func(filter ConnectionFilter) ([]TCPConnection, error) {
		if filter.RemoteCIDR != "" {
			return nil, nil
		}
		return []TCPConnection{{LocalPort: 25565, RemoteAddress: "2606:4700:4700::1111", LossRate: .25}}, nil
	})
	got := (&Service{metrics: metrics}).observeWeighted(context.Background(), route)
	if got.Error != "" || got.Values[0] != .25 {
		t.Fatalf("observation=%#v, want .25", got)
	}
}

func TestPublicRemoteExcludesSpecialUseAddresses(t *testing.T) {
	for _, raw := range []string{"10.1.2.3", "100.64.1.1", "127.0.0.1", "169.254.1.1", "192.168.1.1", "198.18.1.1", "203.0.113.1", "fd00::1", "fe80::1", "2001:db8::1"} {
		if isPublicRemote(raw, map[bool]int{true: 6, false: 4}[net.ParseIP(raw).To4() == nil]) {
			t.Errorf("%s classified public", raw)
		}
	}
	if !isPublicRemote("8.8.8.8", 4) || !isPublicRemote("2606:4700:4700::1111", 6) {
		t.Fatal("public addresses rejected")
	}
}
