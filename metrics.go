package meshroute

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"
)

type TCPConnection struct {
	ID            string  `json:"id"`
	LocalAddress  string  `json:"local_address"`
	LocalPort     uint16  `json:"local_port"`
	RemoteAddress string  `json:"remote_address"`
	RemotePort    uint16  `json:"remote_port"`
	State         string  `json:"state"`
	LossRate      float64 `json:"loss_rate"`
	RTT           uint32  `json:"rtt_us"`
}

type MetricsSource interface {
	Connections(context.Context, string) ([]TCPConnection, error)
}
type MetricsClient struct {
	baseURL, token string
	client         *http.Client
}

func NewMetricsClient(cfg Config) (MetricsSource, error) {
	if cfg.MetricsURL == "" {
		return nil, nil
	}
	pem, err := os.ReadFile(cfg.MetricsCAFile)
	if err != nil {
		return nil, fmt.Errorf("read tcpmetrics CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("tcpmetrics CA contains no certificates")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}, IdleConnTimeout: 60 * time.Second}
	return &MetricsClient{baseURL: cfg.MetricsURL, token: cfg.MetricsToken, client: &http.Client{Transport: transport, Timeout: cfg.ProbeTimeout}}, nil
}

func (m *MetricsClient) Connections(ctx context.Context, pattern string) ([]TCPConnection, error) {
	endpoint := m.baseURL + "/v1/tcp/connections?limit=1000&match=" + url.QueryEscape(pattern)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+m.token)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query tcpmetrics: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tcpmetrics returned %s", resp.Status)
	}
	var payload struct {
		Connections []TCPConnection `json:"connections"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode tcpmetrics response: %w", err)
	}
	return payload.Connections, nil
}
