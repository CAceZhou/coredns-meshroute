package meshroute

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const syncPath = "/v1/mesh/state"

type NodeTransport interface {
	Start(http.Handler) error
	Send(context.Context, Peer, Snapshot) error
	Stop(context.Context) error
}
type HTTPTransport struct {
	cfg      Config
	server   *http.Server
	listener net.Listener
	client   *http.Client
}

func NewHTTPTransport(cfg Config) (*HTTPTransport, error) {
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load mesh TLS keypair: %w", err)
	}
	pem, err := os.ReadFile(cfg.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read mesh CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("mesh CA contains no certificates")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}, RootCAs: pool, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert}
	tr := &http.Transport{TLSClientConfig: tlsConfig.Clone(), MaxIdleConns: 100, MaxIdleConnsPerHost: 4, IdleConnTimeout: 90 * time.Second}
	return &HTTPTransport{cfg: cfg, client: &http.Client{Transport: tr, Timeout: cfg.Interval}}, nil
}
func (t *HTTPTransport) Start(handler http.Handler) error {
	ln, err := net.Listen("tcp", t.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen mesh API: %w", err)
	}
	t.listener = ln
	t.server = &http.Server{Handler: handler, TLSConfig: t.client.Transport.(*http.Transport).TLSClientConfig.Clone(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		if err := t.server.ServeTLS(ln, t.cfg.CertFile, t.cfg.KeyFile); err != nil && err != http.ErrServerClosed {
			log.Errorf("mesh HTTPS listener stopped: %v", err)
		}
	}()
	return nil
}
func (t *HTTPTransport) Send(ctx context.Context, peer Peer, snapshot Snapshot) error {
	body, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, peer.URL+syncPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mesh-Node", t.cfg.NodeID)
	req.Header.Set("X-Mesh-Time", timestamp)
	req.Header.Set("X-Mesh-Signature", signature(t.cfg.HMACKey, req.Method, req.URL.Path, timestamp, body))
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("peer %s returned %s: %s", peer.NodeID, resp.Status, strings.TrimSpace(string(message)))
	}
	return nil
}
func (t *HTTPTransport) Stop(ctx context.Context) error {
	if tr, ok := t.client.Transport.(*http.Transport); ok {
		tr.CloseIdleConnections()
	}
	if t.server == nil {
		return nil
	}
	return t.server.Shutdown(ctx)
}

func signature(key, method, path, timestamp string, body []byte) string {
	sum := sha256.Sum256(body)
	mac := hmac.New(sha256.New, []byte(key))
	fmt.Fprintf(mac, "%s\n%s\n%s\n%s", method, path, timestamp, hex.EncodeToString(sum[:]))
	return hex.EncodeToString(mac.Sum(nil))
}
func verifySignature(key string, r *http.Request, body []byte) bool {
	raw := r.Header.Get("X-Mesh-Time")
	ts, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || abs(time.Now().Unix()-ts) > 30 {
		return false
	}
	expected := signature(key, r.Method, r.URL.Path, raw, body)
	provided, err := hex.DecodeString(r.Header.Get("X-Mesh-Signature"))
	if err != nil {
		return false
	}
	want, _ := hex.DecodeString(expected)
	return hmac.Equal(provided, want)
}
func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
