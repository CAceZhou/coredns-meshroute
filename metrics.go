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
	"strings"
	"time"
)

type TCPConnection struct {
	ID            string  `json:"id"`
	Family        int     `json:"family"`
	LocalAddress  string  `json:"local_address"`
	LocalPort     uint16  `json:"local_port"`
	RemoteAddress string  `json:"remote_address"`
	RemotePort    uint16  `json:"remote_port"`
	State         string  `json:"state"`
	LossRate      float64 `json:"loss_rate"`
	RTT           uint32  `json:"rtt_us"`
}

type ConnectionFilter struct {
	Family      int
	State       string
	LocalPorts  []uint16
	RemotePorts []uint16
	RemoteCIDR  string
	Match       string
}

type MetricsSource interface {
	Connections(context.Context, ConnectionFilter) ([]TCPConnection, error)
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

func (m *MetricsClient) Connections(ctx context.Context, filter ConnectionFilter) ([]TCPConnection, error) {
	query := url.Values{"limit": {"1000"}}
	if filter.Family != 0 {
		query.Set("family", fmt.Sprint(filter.Family))
	}
	if filter.State != "" {
		query.Set("state", filter.State)
	}
	if len(filter.LocalPorts) > 0 {
		query.Set("local_port", joinPorts(filter.LocalPorts))
	}
	if len(filter.RemotePorts) > 0 {
		query.Set("remote_port", joinPorts(filter.RemotePorts))
	}
	if filter.RemoteCIDR != "" {
		query.Set("remote_cidr", filter.RemoteCIDR)
	}
	if filter.Match != "" {
		query.Set("match", filter.Match)
	}
	var result []TCPConnection
	for offset := 0; ; offset += 1000 {
		query.Set("offset", fmt.Sprint(offset))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.baseURL+"/v1/tcp/connections?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+m.token)
		resp, err := m.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("query tcpmetrics: %w", err)
		}
		var payload struct {
			Total       int             `json:"total"`
			Connections []TCPConnection `json:"connections"`
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("tcpmetrics returned %s", resp.Status)
		}
		err = json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode tcpmetrics response: %w", err)
		}
		result = append(result, payload.Connections...)
		if len(result) >= payload.Total || len(payload.Connections) == 0 {
			return result, nil
		}
	}
}

func joinPorts(ports []uint16) string {
	values := make([]string, len(ports))
	for i, p := range ports {
		values[i] = fmt.Sprint(p)
	}
	return strings.Join(values, ",")
}
