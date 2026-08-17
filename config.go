package meshroute

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/coredns/caddy"
)

type Peer struct {
	NodeID string
	URL    string
}
type Config struct {
	NodeID        string
	NodeIP        net.IP
	Listen        string
	Peers         []Peer
	CertFile      string
	KeyFile       string
	CAFile        string
	HMACKey       string
	MetricsURL    string
	MetricsToken  string
	MetricsCAFile string
	Interval      time.Duration
	Timeout       time.Duration
	ProbeTimeout  time.Duration
	Routes        []Route
}

func defaultConfig() Config {
	return Config{Listen: "127.0.0.1:9166", Interval: 5 * time.Second, Timeout: 15 * time.Second, ProbeTimeout: 2 * time.Second}
}

func parse(c *caddy.Controller) (Config, error) {
	cfg := defaultConfig()
	peerIDs := map[string]bool{}
	routeDomains := map[string]bool{}
	for c.Next() {
		if len(c.RemainingArgs()) != 0 {
			return cfg, c.ArgErr()
		}
		for c.NextBlock() {
			property := c.Val()
			args := c.RemainingArgs()
			switch property {
			case "node":
				if len(args) != 2 {
					return cfg, c.ArgErr()
				}
				cfg.NodeID = args[0]
				cfg.NodeIP = net.ParseIP(args[1])
				if cfg.NodeIP == nil {
					return cfg, fmt.Errorf("invalid node IP %q", args[1])
				}
			case "listen":
				if len(args) != 1 {
					return cfg, c.ArgErr()
				}
				cfg.Listen = args[0]
			case "peer":
				if len(args) != 2 {
					return cfg, c.ArgErr()
				}
				if peerIDs[args[0]] {
					return cfg, fmt.Errorf("duplicate peer node ID %q", args[0])
				}
				u, e := url.Parse(args[1])
				if e != nil || u.Scheme != "https" || u.Host == "" {
					return cfg, fmt.Errorf("peer %q must use an absolute https URL", args[0])
				}
				peerIDs[args[0]] = true
				cfg.Peers = append(cfg.Peers, Peer{args[0], strings.TrimRight(args[1], "/")})
			case "tls":
				if len(args) != 3 {
					return cfg, c.ArgErr()
				}
				cfg.CertFile, cfg.KeyFile, cfg.CAFile = args[0], args[1], args[2]
			case "hmac":
				if len(args) != 1 {
					return cfg, c.ArgErr()
				}
				cfg.HMACKey = args[0]
			case "tcpmetrics":
				if len(args) != 3 {
					return cfg, c.ArgErr()
				}
				u, err := url.Parse(args[0])
				if err != nil || u.Scheme != "https" || u.Host == "" {
					return cfg, fmt.Errorf("tcpmetrics must use an absolute https URL")
				}
				cfg.MetricsURL, cfg.MetricsToken, cfg.MetricsCAFile = strings.TrimRight(args[0], "/"), args[1], args[2]
			case "interval":
				d, e := durationArg(args)
				if e != nil {
					return cfg, e
				}
				cfg.Interval = d
			case "timeout":
				d, e := durationArg(args)
				if e != nil {
					return cfg, e
				}
				cfg.Timeout = d
			case "probe_timeout":
				d, e := durationArg(args)
				if e != nil {
					return cfg, e
				}
				cfg.ProbeTimeout = d
			case "route":
				route, e := parseRouteArgs(args)
				if e != nil {
					return cfg, e
				}
				if routeDomains[route.Domain] {
					return cfg, fmt.Errorf("duplicate route domain %q", route.Domain)
				}
				routeDomains[route.Domain] = true
				cfg.Routes = append(cfg.Routes, route)
			default:
				return cfg, c.Errf("unknown meshroute property %q", property)
			}
		}
	}
	if cfg.NodeID == "" || cfg.NodeIP == nil {
		return cfg, fmt.Errorf("node <id> <ip> is required")
	}
	if peerIDs[cfg.NodeID] {
		return cfg, fmt.Errorf("local node ID %q is also configured as peer", cfg.NodeID)
	}
	if _, _, e := net.SplitHostPort(cfg.Listen); e != nil {
		return cfg, fmt.Errorf("invalid listen address: %w", e)
	}
	if cfg.CertFile == "" || cfg.KeyFile == "" || cfg.CAFile == "" {
		return cfg, fmt.Errorf("tls <cert> <key> <ca> is required")
	}
	if len(cfg.HMACKey) < 32 {
		return cfg, fmt.Errorf("hmac key must contain at least 32 bytes")
	}
	needsMetrics := false
	for _, route := range cfg.Routes {
		if route.Source == "connection" {
			needsMetrics = true
		}
	}
	if needsMetrics && (cfg.MetricsURL == "" || len(cfg.MetricsToken) < 16 || cfg.MetricsCAFile == "") {
		return cfg, fmt.Errorf("connection routes require tcpmetrics <https-url> <token> <ca-file> with a token of at least 16 bytes")
	}
	if len(cfg.Routes) == 0 {
		return cfg, fmt.Errorf("at least one route is required")
	}
	if cfg.Interval <= 0 || cfg.Timeout <= cfg.Interval {
		return cfg, fmt.Errorf("timeout must be greater than interval")
	}
	for _, r := range cfg.Routes {
		found := false
		for _, x := range r.Candidates {
			if x.NodeID == cfg.NodeID && x.IP.Equal(cfg.NodeIP) {
				found = true
			}
			if x.NodeID != cfg.NodeID && !peerIDs[x.NodeID] {
				return cfg, fmt.Errorf("route %s candidate %q is not the local node or a configured peer", r.Domain, x.NodeID)
			}
		}
		if !found {
			return cfg, fmt.Errorf("route %s must contain local candidate %s=%s", r.Domain, cfg.NodeID, cfg.NodeIP)
		}
	}
	return cfg, nil
}

// route <domain> <node=ip,...> <connection|probe> <regex|host:port> <loss|latency> <sum|diff|max|min|avg> <min|max> [ttl] [fallback]
func parseRouteArgs(args []string) (Route, error) {
	if len(args) < 7 || len(args) > 9 {
		return Route{}, fmt.Errorf("route expects 7 to 9 arguments")
	}
	r := Route{Domain: normalizeDomain(args[0]), Source: args[2], Target: args[3], Metric: Metric(args[4]), Aggregate: Aggregate(args[5]), Selection: Selection(args[6]), TTL: 5}
	seen := map[string]bool{}
	for _, raw := range strings.Split(args[1], ",") {
		parts := strings.SplitN(raw, "=", 2)
		if len(parts) != 2 || parts[0] == "" || seen[parts[0]] {
			return r, fmt.Errorf("invalid or duplicate candidate %q", raw)
		}
		ip := net.ParseIP(parts[1])
		if ip == nil {
			return r, fmt.Errorf("invalid candidate IP %q", parts[1])
		}
		seen[parts[0]] = true
		r.Candidates = append(r.Candidates, Candidate{parts[0], ip})
	}
	if len(r.Candidates) == 0 {
		return r, fmt.Errorf("route %s has no candidates", r.Domain)
	}
	sortCandidates(r.Candidates)
	if r.Source == "connection" {
		re, e := regexp.Compile(r.Target)
		if e != nil {
			return r, fmt.Errorf("invalid route regex: %w", e)
		}
		r.Matcher = re
	} else if r.Source == "probe" {
		if _, _, e := net.SplitHostPort(r.Target); e != nil {
			return r, fmt.Errorf("invalid probe target: %w", e)
		}
		if r.Metric != MetricLatency {
			return r, fmt.Errorf("probe source supports only latency")
		}
	} else {
		return r, fmt.Errorf("unknown route source %q", r.Source)
	}
	if _, e := ParseScore(string(r.Metric), string(r.Aggregate)); e != nil {
		return r, e
	}
	if r.Selection != SelectMin && r.Selection != SelectMax {
		return r, fmt.Errorf("selection must be min or max")
	}
	if len(args) >= 8 {
		ttl, e := strconv.ParseUint(args[7], 10, 32)
		if e != nil || ttl == 0 {
			return r, fmt.Errorf("invalid route TTL %q", args[7])
		}
		r.TTL = uint32(ttl)
	}
	if len(args) == 9 {
		r.Fallback = net.ParseIP(args[8])
		if r.Fallback == nil {
			return r, fmt.Errorf("invalid fallback IP %q", args[8])
		}
	}
	return r, nil
}
func durationArg(args []string) (time.Duration, error) {
	if len(args) != 1 {
		return 0, fmt.Errorf("expected one duration")
	}
	d, e := time.ParseDuration(args[0])
	if e != nil {
		return 0, e
	}
	return d, nil
}
