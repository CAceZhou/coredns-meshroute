# coredns-meshroute

meshroute 是一个 CoreDNS 外置插件，用于在多个同级 DNS 节点间交换版本化观测状态，并以完全相同的评分规则为指定域名选择最佳节点 IP。

节点之间使用 HTTPS、TLS 和 HMAC 同步状态。它提供的是无网络分区时的确定性最终一致性：状态收敛后，任意节点对相同查询会选出同一候选节点；它不是 Raft 等强一致协议，也不创建 VPN、隧道或业务流量转发。

## 功能

- 配置多个同级节点，对所有 peer 建立 TLS 连接、心跳、重连和全量状态同步。
- 版本号、来源节点 ID、更新时间驱动状态合并，重复和乱序消息安全。
- 固定健康度、有效指标数、节点 ID 的 tie-breaker，确保相同输入产生相同结果。
- 支持按已建立 TCP 连接的近似丢包率选路，或按主动 TCP 建连延迟选路。
- 支持 sum、diff、max、min、avg 聚合，最终可选最小或最大分数。
- 支持 weighted_route：按两组 TCP 样本、端口平权和组权重综合计算丢包率。
- A 与 AAAA 使用独立候选 IP、独立路由键和独立评分。
- 未匹配 DNS 查询继续交给下游插件，例如 forward。

## 一致性模型与边界

每个节点只发布自身采集到的观测。收到 peer 状态后按版本合并，并周期性广播全量快照。候选节点只有健康、状态未过期且存在有效指标时才参与评分。

在没有网络分区、所有节点拥有相同配置且状态已收敛时，以下条件可保证一致回答：

1. node ID、候选 IP、路由规则和权重在所有节点完全一致。
2. 所有节点使用相同的失效超时。
3. 节点时钟没有极端偏差。

网络分区期间各分区可能产生不同回答；恢复连接后会通过快照同步重新收敛。

## 前置条件

- Go 1.25 或更新版本；CI 使用 CoreDNS 1.14.6。
- 节点间可达的 TCP 9166，或自定义监听端口。
- 所有节点共享同一 HMAC 密钥，且证书由同一受信 CA 签发。
- 使用连接指标或加权路由时，需要本机 coredns-tcpmetrics HTTPS API。

## 编译到 CoreDNS

在 CoreDNS 源码树 plugin.cfg 的 forward 前添加：

~~~text
meshroute:github.com/CAceZhou/coredns-meshroute
~~~

如同时使用连接指标，还需注册：

~~~text
tcpmetrics:github.com/CAceZhou/coredns-tcpmetrics
~~~

然后执行：

~~~bash
go generate
go build -o coredns .
~~~

两个插件必须编入同一个 CoreDNS 二进制。GitHub Actions 会自动测试插件并产出 Linux amd64、arm64 二进制。

## 基础 Corefile

以下展示节点 edge-a 的最小 mTLS 配置：

~~~text
. {
    meshroute {
        node edge-a 192.0.2.10 2001:db8::10
        listen 0.0.0.0:9166
        peer edge-b https://edge-b.example.net:9166
        peer edge-c https://edge-c.example.net:9166
        tls /etc/coredns/tls/edge-a.crt /etc/coredns/tls/edge-a.key /etc/coredns/tls/cluster-ca.crt
        hmac 请替换为至少32字节的随机共享密钥
        interval 5s
        timeout 15s
        probe_timeout 2s

        route service.example.net edge-a=192.0.2.10,edge-b=192.0.2.11,edge-c=192.0.2.12 probe origin.example.net:443 latency avg min 5
    }
    forward . 1.1.1.1
}
~~~

meshroute 对匹配 route 的 A/AAAA 查询直接写入选择结果；不匹配名称、非 A/AAAA 查询会继续交给下游插件。没有可用候选时默认 SERVFAIL，配置 fallback 后则返回备用 IP。

当匹配路由没有任何有效指标样本、但仍有健康且未过期节点时，meshroute 会在这些节点中等概率随机选择，并在 CoreDNS 日志中记录 `no valid metric samples`。这属于无样本降级行为；一旦样本收敛，将恢复按评分选择。

节点公网地址可用 `public_ip auto` 自动发现，或用 `public_ip <IPv4> [IPv6]` 固定。自动发现只使用外部 IPv4/IPv6 探测接口，不依赖本地 DNS 或 DDNS 记录；IPv6 包括 `api6.ipify.org`、`v6.ident.me`。

## 配置指令

| 指令 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| node <id> <ipv4> [ipv6] | 是 | 无 | 本节点标识和可解析 IP。双栈 weighted route 需要 IPv4、IPv6。 |
| listen <host:port> | 否 | 127.0.0.1:9166 | 节点间 HTTPS 监听地址。多节点部署应监听可达地址。 |
| peer <id> <https-url> | 否 | 无 | peer 的绝对 HTTPS URL，不可与本节点 ID 重复。 |
| tls <cert> <key> <ca> | 是 | 无 | 节点 mTLS 证书、私钥和验证 peer 的 CA。 |
| hmac <secret> | 是 | 无 | 至少 32 字节的共享密钥。 |
| tcpmetrics <url> <token> <ca> | 连接类 route 必填 | 无 | 本机 tcpmetrics HTTPS 地址、Token 和其 CA。 |
| interval <duration> | 否 | 5s | 发布本地状态和同步周期。 |
| timeout <duration> | 否 | 15s | peer 状态失效时限，必须大于 interval。 |
| probe_timeout <duration> | 否 | 2s | 主动 TCP 探测超时。 |
| route | 至少一条 | 无 | 通用连接/延迟选路规则。 |
| weighted_route | 至少一条 | 无 | 加权双组 TCP 丢包率选路规则。 |

加载配置时会拒绝重复 peer ID、非法 HTTPS URL、TLS/HMAC 缺失、无候选 route、无效 CIDR/正则/表达式、候选不属于本节点或已配置 peer，以及双栈路由缺失本地对应地址等错误。

## 通用 route 语法

~~~text
route <domain> <node=ip,...> <connection|probe> <regex|host:port> <loss|latency> <sum|diff|max|min|avg> <min|max> [ttl] [fallback-ip]
~~~

- domain：要接管回答的 DNS 名称，尾部点可省略。
- node=ip：候选节点；每个节点都必须配置自身与所有 peer 的同一映射。
- connection：从本机 tcpmetrics 查询连接，并让正则匹配连接 ID、本地地址或远端地址。
- probe：主动测量到 host:port 的 TCP 建连耗时，只能配合 latency。
- loss：连接的 TCP 重传率近似丢包率。
- latency：主动建连延迟。
- 聚合方式：sum、diff、max、min、avg。
- 最终选择：min 选择最低分节点，max 选择最高分节点。
- ttl：DNS TTL 秒数，默认 5。
- fallback-ip：无有效候选时返回的 IP；未给出则 SERVFAIL。

示例：取所有匹配 HTTPS 已建立连接的平均丢包率，返回最低者：

~~~text
route api.example.net node1=203.0.113.10,node2=203.0.113.11 connection ^.*:443.*ESTABLISHED loss avg min 5
~~~

## FDRS 双节点双栈加权示例

项目内的生成器会生成 node1/node2 的 CA、证书、私钥和 Corefile：

~~~bash
go run ./cmd/fdrs-bundle -out dist/fdrs-dual-node
~~~

如节点使用不同的 IPv4 地址，可通过参数写入节点配置和证书 SAN：

~~~bash
go run ./cmd/fdrs-bundle -force -out dist/fdrs-dual-node \\
  -node1-ipv4 10.144.144.101 -node2-ipv4 10.144.144.100 \\
  -node1-peer 10.144.144.100 -node2-peer 10.144.144.101
~~~

生成后的敏感文件位于 dist/fdrs-dual-node/ca/，该目录已被 Git 忽略。不要把它上传到 GitHub、对象存储或聊天记录。

FDRS 规则：

~~~text
weighted_route fdrs.solitarymc.top ipv4 node1=198.18.1.122,node2=198.18.1.123 target_cidr=10.0.0.0/16 ports=25565,25566 target_weight=0.6 public_weight=0.4 select=min ttl=5
weighted_route fdrs.solitarymc.top ipv6 node1=fdfe:dcba:9876::173,node2=fdfe:dcba:9876::174 target_cidr=10.0.0.0/16 ports=25565,25566 target_weight=0.6 public_weight=0.4 select=min ttl=5
~~~

评分过程：

1. 目标发送组，权重 0.6：查询 IPv4、远端地址属于 10.0.0.0/16、远端端口为 25565 或 25566 的已建立连接。
2. 外部联入组，权重 0.4：查询与 DNS 路由同地址族、本地端口为 25565 或 25566 的已建立连接，只保留远端为公网单播地址的连接。
3. 外部联入会排除私网、回环、链路本地、CGNAT、文档地址、基准测试地址、多播及其他 IANA 特殊用途范围。
4. 每个端口先独立计算平均 loss_rate；两个端口再等权平均，避免连接数量多的端口主导结果。
5. 两组按 0.6/0.4 加权；某端口或某组没有样本时，对剩余有效权重重新归一化；完全没有样本的节点不参与。
6. IPv6 route 的目标发送组刻意复用上述 IPv4 目标网络，外部联入组使用 IPv6 连接。
7. A fdrs.solitarymc.top 只使用 IPv4 route，AAAA 只使用 IPv6 route；二者独立选择最低分节点。

198.18.0.0/15 为文档/基准测试用途地址。实际部署必须替换成 node1/node2 可从 DNS 客户端访问的真实地址，并确保 node1.solitarymc.top、node2.solitarymc.top 能解析并连接到对方的 9166 端口。

## 双节点部署步骤

在构建机生成交付目录：

~~~bash
go run ./cmd/fdrs-bundle -out dist/fdrs-dual-node
go run ./cmd/fdrs-bundle -checksums-only -out dist/fdrs-dual-node
~~~

在 node1：

~~~bash
install -m 0755 bin/coredns-linux-amd64 /usr/local/bin/coredns
install -d -m 0700 /etc/coredns/tls
install -m 0600 node1/Corefile /etc/coredns/Corefile
install -m 0600 node1/tls/* /etc/coredns/tls/
/usr/local/bin/coredns -conf /etc/coredns/Corefile
~~~

node2 使用 node2 目录；ARM 主机改用 coredns-linux-arm64。为采集全机 TCP socket，建议以 root 启动 CoreDNS。

防火墙至少允许：

- 客户端到节点：TCP/UDP 53。
- node1 与 node2 双向：TCP 9166。
- 不要对外开放 TCP 9165；默认它仅供本机 meshroute 访问。

部署前校验交付包：

~~~bash
sha256sum -c checksums.sha256
~~~

## DNS 行为和排障

- route 命中且有有效候选：直接返回获选节点 IP 与 route TTL。
- route 命中但无有效候选：返回 fallback，或 SERVFAIL。
- 未命中 route 或查询类型非 A/AAAA：调用下游插件。
- 同分时顺序固定为：健康状态、有效指标数量、节点 ID 字典序。

排障顺序：

1. 确认两节点 peer URL 可以双向 HTTPS 访问，且证书 SAN 匹配 node 域名。
2. 确认两节点 HMAC、候选映射、route 规则和 CA 文件完全一致。
3. 调用本机 tcpmetrics 的 summary 与 connections API，确认采样覆盖预期连接。
4. 检查 9166 的 TLS、防火墙和系统时间。
5. 没有 TCP 样本时，该节点不会参与加权 route；不会自动视为丢包 0。

## 开发与安全

~~~bash
go test ./...
go vet ./...
make check
~~~

- 节点间通信必须使用 HTTPS；应使用私有 CA、短期证书和至少 32 字节随机 HMAC。
- tcpmetrics Token、Corefile 中的 HMAC 与私钥均为敏感数据。
- 本插件只负责观测、状态同步和 DNS 回答，不负责修改系统路由、建立 VPN 或转发业务流量。
