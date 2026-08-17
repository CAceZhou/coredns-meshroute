package meshroute

import (
	"testing"

	"github.com/coredns/caddy"
)

func TestParseRouteArgs(t *testing.T) {
	route, err := parseRouteArgs([]string{"service.example.", "a=192.0.2.1,b=192.0.2.2", "connection", `:443.*ESTABLISHED`, "loss", "max", "min", "10", "192.0.2.9"})
	if err != nil {
		t.Fatal(err)
	}
	if route.Domain != "service.example." || route.TTL != 10 || len(route.Candidates) != 2 || route.Matcher == nil {
		t.Fatalf("unexpected route: %#v", route)
	}
}

func TestParseRouteRejectsInvalidProbeMetric(t *testing.T) {
	if _, err := parseRouteArgs([]string{"service.example.", "a=192.0.2.1", "probe", "origin:443", "loss", "avg", "min"}); err == nil {
		t.Fatal("probe loss metric accepted")
	}
}

func TestParseConfig(t *testing.T) {
	c := caddy.NewTestController("dns", `meshroute {
        node a 192.0.2.1
        listen 127.0.0.1:9443
        peer b https://b.example:9443
        tls node.crt node.key ca.crt
        hmac 0123456789abcdef0123456789abcdef
        route service.example a=192.0.2.1,b=192.0.2.2 probe origin.example:443 latency avg min
    }`)
	cfg, err := parse(c)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NodeID != "a" || len(cfg.Peers) != 1 || len(cfg.Routes) != 1 {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestParseConfigRejectsUnknownCandidate(t *testing.T) {
	c := caddy.NewTestController("dns", `meshroute {
        node a 192.0.2.1
        peer b https://b.example:9443
        tls node.crt node.key ca.crt
        hmac 0123456789abcdef0123456789abcdef
        route service.example a=192.0.2.1,c=192.0.2.3 probe origin.example:443 latency avg min
    }`)
	if _, err := parse(c); err == nil {
		t.Fatal("unknown candidate accepted")
	}
}

func TestConnectionRouteRequiresMetricsAPI(t *testing.T) {
	c := caddy.NewTestController("dns", `meshroute {
        node a 192.0.2.1
        tls node.crt node.key ca.crt
        hmac 0123456789abcdef0123456789abcdef
        route service.example a=192.0.2.1 connection :443 loss avg min
    }`)
	if _, err := parse(c); err == nil {
		t.Fatal("connection route without tcpmetrics API accepted")
	}
}
