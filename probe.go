package meshroute

import (
	"context"
	"net"
	"time"
)

type Probe interface {
	Measure(context.Context, string) (time.Duration, error)
}
type TCPProbe struct{ Timeout time.Duration }

func (p TCPProbe) Measure(ctx context.Context, target string) (time.Duration, error) {
	d := net.Dialer{Timeout: p.Timeout}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", target)
	elapsed := time.Since(start)
	if err != nil {
		return 0, err
	}
	_ = conn.Close()
	return elapsed, nil
}
