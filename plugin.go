// Package meshroute implements deterministic, eventually consistent DNS routing.
package meshroute

import (
	"context"
	"time"

	"github.com/coredns/caddy"
	"github.com/coredns/coredns/core/dnsserver"
	"github.com/coredns/coredns/plugin"
	clog "github.com/coredns/coredns/plugin/pkg/log"
	"github.com/coredns/coredns/request"
	"github.com/miekg/dns"
)

const name = "meshroute"

var log = clog.NewWithPlugin(name)

func init() { plugin.Register(name, setup) }
func setup(c *caddy.Controller) error {
	cfg, err := parse(c)
	if err != nil {
		return plugin.Error(name, err)
	}
	service, err := NewService(cfg)
	if err != nil {
		return plugin.Error(name, err)
	}
	c.OnStartup(service.Start)
	c.OnShutdown(service.Stop)
	dnsserver.GetConfig(c).AddPlugin(func(next plugin.Handler) plugin.Handler { return &MeshRoute{Next: next, cfg: cfg, service: service} })
	return nil
}

type MeshRoute struct {
	Next    plugin.Handler
	cfg     Config
	service *Service
}

func (m *MeshRoute) Name() string { return name }
func (m *MeshRoute) Ready() bool  { return m.service.Ready() }
func (m *MeshRoute) ServeDNS(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	if len(r.Question) == 0 {
		return plugin.NextOrFailure(name, m.Next, ctx, w, r)
	}
	qname := normalizeDomain(r.Question[0].Name)
	var route *Route
	for i := range m.cfg.Routes {
		if m.cfg.Routes[i].Domain == qname {
			route = &m.cfg.Routes[i]
			break
		}
	}
	if route == nil {
		return plugin.NextOrFailure(name, m.Next, ctx, w, r)
	}
	qtype := r.Question[0].Qtype
	if qtype != dns.TypeA && qtype != dns.TypeAAAA {
		return plugin.NextOrFailure(name, m.Next, ctx, w, r)
	}
	selected := *route
	selected.Candidates = selected.Candidates[:0]
	for _, c := range route.Candidates {
		if (qtype == dns.TypeA && c.IP.To4() != nil) || (qtype == dns.TypeAAAA && c.IP.To4() == nil) {
			selected.Candidates = append(selected.Candidates, c)
		}
	}
	candidate, _, ok := m.service.store.Best(selected, time.Now().UTC(), m.cfg.Timeout)
	ip := candidate.IP
	if !ok {
		ip = route.Fallback
		if ip == nil || ((qtype == dns.TypeA) != (ip.To4() != nil)) {
			return dns.RcodeServerFailure, nil
		}
	}
	msg := new(dns.Msg)
	msg.SetReply(r)
	msg.Authoritative = true
	state := request.Request{W: w, Req: r}
	header := dns.RR_Header{Name: state.QName(), Class: r.Question[0].Qclass, Ttl: route.TTL}
	if qtype == dns.TypeA {
		header.Rrtype = dns.TypeA
		msg.Answer = []dns.RR{&dns.A{Hdr: header, A: ip.To4()}}
	} else {
		header.Rrtype = dns.TypeAAAA
		msg.Answer = []dns.RR{&dns.AAAA{Hdr: header, AAAA: ip}}
	}
	if err := w.WriteMsg(msg); err != nil {
		return dns.RcodeServerFailure, err
	}
	return dns.RcodeSuccess, nil
}
