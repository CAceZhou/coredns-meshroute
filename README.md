# coredns-meshroute

`meshroute` is an out-of-tree CoreDNS plugin that exchanges versioned node observations over mTLS and HMAC-authenticated HTTPS, computes deterministic scores, and answers configured names with the selected node address.

The protocol is eventually consistent rather than a consensus protocol. With an intact network, periodic full snapshots and deterministic node-ID tie-breaking make answers converge across all nodes.

## Build with CoreDNS

Add the plugin before `forward` in CoreDNS `plugin.cfg`:

```text
meshroute:github.com/CAceZhou/coredns-meshroute
```

Connection-based rules query a local `coredns-tcpmetrics` HTTPS API. Probe-only configurations can use `meshroute` alone.

## Corefile

```text
meshroute {
    node edge-a 192.0.2.10 2001:db8::10
    listen 0.0.0.0:9166
    peer edge-b https://edge-b.internal:9166
    peer edge-c https://edge-c.internal:9166
    tls /etc/coredns/node-a.crt /etc/coredns/node-a.key /etc/coredns/cluster-ca.crt
    hmac replace-with-at-least-32-random-bytes
    tcpmetrics https://127.0.0.1:9165 replace-with-the-tcpmetrics-token /etc/coredns/metrics-ca.crt
    interval 5s
    timeout 15s
    probe_timeout 2s

    route service.example.net edge-a=192.0.2.10,edge-b=192.0.2.11,edge-c=192.0.2.12 probe origin.example.net:443 latency avg min 5
    route stream.example.net edge-a=192.0.2.10,edge-b=192.0.2.11,edge-c=192.0.2.12 connection ^.*:443.*ESTABLISHED loss max min 5 192.0.2.10
}
```

## Weighted dual-stack connection routing

The FDRS deployment uses two routes for one DNS name. A queries use the IPv4 route and AAAA queries use the IPv6 route:

```text
weighted_route fdrs.solitarymc.top ipv4 node1=198.18.1.122,node2=198.18.1.123 target_cidr=10.0.0.0/16 ports=25565,25566 target_weight=0.6 public_weight=0.4 select=min ttl=5
weighted_route fdrs.solitarymc.top ipv6 node1=fdfe:dcba:9876::173,node2=fdfe:dcba:9876::174 target_cidr=10.0.0.0/16 ports=25565,25566 target_weight=0.6 public_weight=0.4 select=min ttl=5
```

- The target group contains IPv4 connections whose remote address is in `target_cidr` and whose remote port is listed in `ports`.
- The public-client group contains connections of the DNS route's address family whose local port is listed in `ports` and whose remote address is not in an IANA special-use range.
- IPv6 routing intentionally reuses the IPv4 target group while using IPv6 public-client samples.
- Each port is averaged independently and available port means are equally weighted. Missing ports or groups are reweighted over the remaining data; a node with no samples does not participate.

Generate the node-specific certificates and Corefiles with:

```bash
go run ./cmd/fdrs-bundle -out dist/fdrs-dual-node
```

Route syntax:

```text
route <domain> <node=ip,...> <connection|probe> <regex|host:port> <loss|latency> <sum|diff|max|min|avg> <min|max> [ttl] [fallback-ip]
```

Every node must use the same route definitions and node-ID/IP mapping. TLS certificates must be signed by the configured cluster CA, and the HMAC secret must contain at least 32 bytes.

## Development

Go 1.25 or newer is required.

```bash
make check
```
