package meshroute

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		observations[route.Domain] = s.observe(ctx, route)
	}
	s.store.SetLocal(NodeState{NodeID: s.cfg.NodeID, IP: s.cfg.NodeIP.String(), Version: s.version, UpdatedAt: time.Now().UTC(), Healthy: true, Observations: observations})
}
func (s *Service) observe(ctx context.Context, route Route) Observation {
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
	connections, err := s.metrics.Connections(ctx, route.Target)
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
