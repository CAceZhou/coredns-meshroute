package meshroute

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net"
	"strings"
	"sync"
	"time"
)

type Service struct {
	cfg       Config
	store     *MemoryStore
	probe     Probe
	metrics   MetricsSource
	transport NodeTransport
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	version   uint64
	peerIDs   map[string]bool
}

func NewService(cfg Config) (*Service, error) {
	tr, err := NewHTTPTransport(cfg)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(cfg.Peers))
	for _, p := range cfg.Peers {
		ids[p.NodeID] = true
	}
	metrics, err := NewMetricsClient(cfg)
	if err != nil {
		return nil, err
	}
	return &Service{cfg: cfg, store: NewMemoryStore(), probe: TCPProbe{Timeout: cfg.ProbeTimeout}, metrics: metrics, transport: tr, peerIDs: ids}, nil
}
func newServiceWithDependencies(cfg Config, store *MemoryStore, probe Probe, metrics MetricsSource, tr NodeTransport) *Service {
	ids := map[string]bool{}
	for _, p := range cfg.Peers {
		ids[p.NodeID] = true
	}
	return &Service{cfg: cfg, store: store, probe: probe, metrics: metrics, transport: tr, peerIDs: ids}
}

func (s *Service) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.publish(ctx)
	if err := s.transport.Start(s.routes()); err != nil {
		cancel()
		return err
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.cfg.Interval)
		defer ticker.Stop()
		s.broadcast(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.publish(ctx)
				s.broadcast(ctx)
			}
		}
	}()
	return nil
}
func (s *Service) Stop() error {
	if s.cancel == nil {
		return nil
	}
	s.cancel()
	s.wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.transport.Stop(ctx)
}
func (s *Service) Ready() bool {
	for _, st := range s.store.Snapshot() {
		if st.NodeID == s.cfg.NodeID {
			return true
		}
	}
	return false
}

func (s *Service) publish(ctx context.Context) {
	s.version++
	observations := make(map[string]Observation, len(s.cfg.Routes))
	for _, route := range s.cfg.Routes {
		observations[route.Key()] = s.observe(ctx, route)
	}
	public4, public6 := s.publicIPs(ctx)
	s.store.SetLocal(NodeState{NodeID: s.cfg.NodeID, IP: s.cfg.NodeIP.String(), PublicIPv4: public4, PublicIPv6: public6, Version: s.version, UpdatedAt: time.Now().UTC(), Healthy: true, Observations: observations})
}

func (s *Service) publicIPs(ctx context.Context) (string, string) {
	if !s.cfg.PublicAuto { return ipString(s.cfg.PublicIPv4), ipString(s.cfg.PublicIPv6) }
	return fetchPublicIP(ctx, 4), fetchPublicIP(ctx, 6)
}

func ipString(ip net.IP) string { if ip == nil { return "" }; return ip.String() }

func fetchPublicIP(ctx context.Context, family int) string {
	endpoints := []string{"https://api.ipify.org", "https://v4.ident.me", "https://ifconfig.me/ip", "https://icanhazip.com"}
	if family == 6 {
		endpoints = []string{"https://api6.ipify.org", "https://v6.ident.me", "https://ifconfig.co/ip"}
	}
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "udp4", "1.1.1.1:53")
	}}
	transport := &http.Transport{DialContext: func(dialCtx context.Context, _, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil { return nil, err }
		ips, err := resolver.LookupIP(dialCtx, "ip", host)
		if err != nil {
			ips, err = net.DefaultResolver.LookupIP(dialCtx, "ip", host)
		}
		if err != nil { return nil, err }
		for _, ip := range ips {
			if family == 4 && ip.To4() == nil { continue }
			if family == 6 && (ip.To4() != nil || ip.To16() == nil) { continue }
			conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(dialCtx, "tcp", net.JoinHostPort(ip.String(), port))
			if err == nil { return conn, nil }
		}
		return nil, fmt.Errorf("no IPv%d address for %s", family, host)
	}}
	client := &http.Client{Timeout: 5 * time.Second, Transport: transport}
	for _, endpoint := range endpoints {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil); if err != nil { continue }
		if family == 4 { req.Header.Set("User-Agent", "ddns-go/meshroute-ipv4") } else { req.Header.Set("User-Agent", "ddns-go/meshroute-ipv6") }
		resp, err := client.Do(req); if err != nil { continue }
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 128)); resp.Body.Close(); if readErr != nil || resp.StatusCode != http.StatusOK { continue }
		value := strings.TrimSpace(string(body)); ip := net.ParseIP(value); if ip == nil || (family == 4 && ip.To4() == nil) || (family == 6 && (ip.To4() != nil || ip.To16() == nil)) { continue }
		return ip.String()
	}
	log.Warningf("unable to discover public IPv%d address; retaining configured candidate IP", family)
	return ""
}
func (s *Service) observe(ctx context.Context, route Route) Observation {
	if route.Weighted != nil {
		return s.observeWeighted(ctx, route)
	}
	if route.Source == "probe" {
		duration, err := s.probe.Measure(ctx, route.Target)
		if err != nil {
			return Observation{Error: err.Error()}
		}
		return Observation{Values: []float64{float64(duration) / float64(time.Millisecond)}}
	}
	if s.metrics == nil {
		return Observation{Error: "tcpmetrics client is not configured"}
	}
	connections, err := s.metrics.Connections(ctx, ConnectionFilter{Match: route.Target})
	if err != nil {
		return Observation{Error: err.Error()}
	}
	var values []float64
	for _, conn := range connections {
		key := fmt.Sprintf("%s:%d %s:%d %s %s", conn.LocalAddress, conn.LocalPort, conn.RemoteAddress, conn.RemotePort, conn.State, conn.ID)
		if !route.Matcher.MatchString(key) {
			continue
		}
		if route.Metric == MetricLoss {
			values = append(values, conn.LossRate)
		} else {
			values = append(values, float64(conn.RTT)/1000)
		}
	}
	if len(values) == 0 {
		return Observation{Error: "no matching TCP connections"}
	}
	return Observation{Values: values}
}

func (s *Service) observeWeighted(ctx context.Context, route Route) Observation {
	if s.metrics == nil {
		return Observation{Error: "tcpmetrics client is not configured"}
	}
	spec := route.Weighted
	target, err := s.metrics.Connections(ctx, ConnectionFilter{Family: 4, State: "ESTABLISHED", RemotePorts: spec.Ports, RemoteCIDR: spec.TargetCIDR.String()})
	if err != nil {
		return Observation{Error: err.Error()}
	}
	publicConnections, err := s.metrics.Connections(ctx, ConnectionFilter{Family: route.Family, State: "ESTABLISHED", LocalPorts: spec.Ports})
	if err != nil {
		return Observation{Error: err.Error()}
	}
	public := publicConnections[:0]
	for _, connection := range publicConnections {
		if isPublicRemote(connection.RemoteAddress, route.Family) {
			public = append(public, connection)
		}
	}
	targetScore, hasTarget := equalPortLoss(target, spec.Ports, false)
	publicScore, hasPublic := equalPortLoss(public, spec.Ports, true)
	weighted, ok := reweightedScore(targetScore, hasTarget, spec.TargetWeight, publicScore, hasPublic, spec.PublicWeight)
	if !ok {
		return Observation{Error: "no target-network or public-client TCP samples"}
	}
	return Observation{Values: []float64{weighted}}
}

func equalPortLoss(connections []TCPConnection, ports []uint16, local bool) (float64, bool) {
	var portMeans []float64
	for _, port := range ports {
		var sum float64
		count := 0
		for _, connection := range connections {
			selected := connection.RemotePort
			if local {
				selected = connection.LocalPort
			}
			if selected == port {
				sum += connection.LossRate
				count++
			}
		}
		if count > 0 {
			portMeans = append(portMeans, sum/float64(count))
		}
	}
	if len(portMeans) == 0 {
		return 0, false
	}
	sum := 0.0
	for _, mean := range portMeans {
		sum += mean
	}
	return sum / float64(len(portMeans)), true
}

func reweightedScore(a float64, hasA bool, weightA float64, b float64, hasB bool, weightB float64) (float64, bool) {
	weighted, total := 0.0, 0.0
	if hasA && weightA > 0 {
		weighted += a * weightA
		total += weightA
	}
	if hasB && weightB > 0 {
		weighted += b * weightB
		total += weightB
	}
	if total == 0 {
		return 0, false
	}
	return weighted / total, true
}
func (s *Service) broadcast(ctx context.Context) {
	snapshot := Snapshot{Sender: s.cfg.NodeID, States: s.store.Snapshot()}
	var sends sync.WaitGroup
	for _, peer := range s.cfg.Peers {
		peer := peer
		sends.Add(1)
		go func() {
			defer sends.Done()
			sendCtx, cancel := context.WithTimeout(ctx, s.cfg.Interval)
			defer cancel()
			if err := s.transport.Send(sendCtx, peer, snapshot); err != nil && ctx.Err() == nil {
				log.Warningf("state sync to %s failed: %v", peer.NodeID, err)
			}
		}()
	}
	sends.Wait()
}

func (s *Service) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(syncPath, s.handleSync)
	return mux
}
func (s *Service) handleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sender := r.Header.Get("X-Mesh-Node")
	if !s.peerIDs[sender] {
		http.Error(w, "unknown peer", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !verifySignature(s.cfg.HMACKey, r, body) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	var snapshot Snapshot
	if err := json.Unmarshal(body, &snapshot); err != nil || snapshot.Sender != sender {
		http.Error(w, "invalid snapshot", http.StatusBadRequest)
		return
	}
	for _, st := range snapshot.States {
		if st.NodeID == "" || st.Version == 0 {
			http.Error(w, "invalid node state", http.StatusBadRequest)
			return
		}
	}
	s.store.Merge(snapshot.States)
	w.WriteHeader(http.StatusNoContent)
}
