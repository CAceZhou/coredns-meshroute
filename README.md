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
    node edge-a 192.0.2.10
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
