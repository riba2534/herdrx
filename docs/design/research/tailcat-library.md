# tailcat 作为 Go 库嵌入 herdr 远程隧道的调研报告

- 调研对象：`../tailcat`（git HEAD `476c217fa`，最新 tag `v0.5.0`）
- 依赖基线：`go 1.27.0`，`tailscale.com v1.103.0-pre.0.20260830144538-72780705eda8`（go.mod:3,22）
- 范围：只读调研，未改任何文件。所有结论附 `文件:行号` 证据；标注「上游行为」的条目来自 tailscale.com 通用知识，本仓库内没有直接代码证据。
- 章节顺序按团队负责人指定优先级：C → B → A → D → E → H → G → F → I。

---

## C. 地址与密钥模型

### C.1 tailcat 地址由什么派生

一个 tailcat 地址（Go 类型 `tailcat.Addr`，字符串，`tc` 前缀 + base64url(CBOR)）编码的是 `ConnInfo`（tailcat.go:136-170）：

| 字段 | 内容 | 来源 |
|---|---|---|
| `ServerPublic` | 服务端 WireGuard 公钥（Curve25519, 32B） | `PrivateKey.Private.Public()`（tailcat.go:238） |
| `ServerDiscoPublic` | 独立的路径发现（disco）公钥，32B | 由节点私钥 HMAC-SHA256 确定性派生（tailcat.go:1959-1969），不可从 disco 公钥反推节点公钥 |
| `Region` | 完整 DERP 区域元数据（主机名 / IPv4 / IPv6 / 端口） | 「长地址」形式 |
| `RegionID` | 默认 DERP map 里的区域整数 ID | 「短地址」形式；`-1` 表示「启动时自动挑」（tailcat.go:167-169） |

CBOR 字段名是单字符（`p`/`k`/`r`/`i`/…，wire.go:26-59），并且 `wire_test.go:22-38` 锁定了这些短名不能变——这就是 wire 格式。

**重要设计点：地址 = 能力（capability）。** 谁拿到地址谁就能连（在没有 `--allow` 时）。`ServerPublic` 是「不可猜测的秘密」，所以 disco 公钥必须独立，避免直连 UDP 路径上的明文 disco 帧泄漏它（tailcat.go:147-150，SECURITY.md:59-62 记录了这个漏洞的修复）。

服务端在隧道内的 IPv6 地址也由公钥派生：`fd7a:115c:a1e0::/48` + 公钥前 10 字节（`tcAddrForKey`，tailcat.go:1434-1447）。README 明确说这是实现细节可能会变（README.md:609-614）。

### C.2 短地址 vs 长地址（resolve）

| | 短地址 | 长地址（resolved / `--full-address`） |
|---|---|---|
| 内容 | 公钥 ×2 + `RegionID` 整数 | 公钥 ×2 + 嵌入的 DERP 节点（最多 2 个，tailcat.go:798-801） |
| 长度 | 约 95 字节（README.md:549） | 更长，README 示例约 180 字节 |
| 客户端首连 | 必须先 HTTPS 拉一次 DERP map（`ConnInfo.Expand`，tailcat.go:1066-1116，超时 10s） | 零网络查询，直接连 DERP |
| 对 DERP map 变更的鲁棒性 | 依赖 map 中该 RegionID 仍存在 | 绑定了 resolve 时刻的主机名/IP（README.md:786 类比 DNS 解析） |
| 生成方式 | `ci.Addr()` 只填 `RegionID` | `Addr.Resolve(ctx, opts...)`（tailcat.go:787-804）或 `Server.TailcatAddr()`（永远输出长地址，tailcat.go:694-700） |

注意：**库层 `Server.TailcatAddr()` 一律返回嵌入 Region 的长地址**（`lb.tailcatAddr()` 把 `lb.dm.Regions` 全塞进去，tailcat.go:719-736）。CLI 的短地址是 `cmd/tailcat/tailcat.go:1185-1210` 自己另外组的 `ConnInfo`。所以嵌入库时，如果想给二维码一个短地址，需要自己构造 `ConnInfo{ServerPublic, ServerDiscoPublic, RegionID}` 再 `.Addr()`。

自建 DERP 时地址天然是长地址：`genkey --region=derp.example.com` 直接把主机名写进 `Region[0].Nodes[0].HostName`（cmd/tailcat/tailcat.go:1647-1655），没有 RegionID 可引用。

### C.3 为什么「固定 region」重要

`Server.Start()` 的逻辑（tailcat.go:407-424）：

1. `Region != nil` → 直接用，不拉 map。
2. 否则 `RegionID` 非 0 → 拉 map 取该区域。
3. 否则 `RegionID == 0` 被 `cmp.Or(s.RegionID, -1)` 变成 `-1` → 拉 map + `PickBestRegion` 做 netcheck 延迟探测挑最近的（tailcat.go:1081-1108）。

服务端和客户端**必须在同一个 DERP 区域会合**：meow 握手包是 `mc.SendDERPPacketTo(dstNode, derpRegion, pkt)` 定向发到那个区域（tailcat.go:1789）。如果服务端每次重启都重新 netcheck，可能选到不同区域，已经发出去的地址就失效了。所以：

- 要发布/持久化的地址（二维码、DNS TXT、数据库里存的），**必须**用固定 region（`genkey --fixed-region` 或 `--region=<name>`，README.md:415-423）。
- `PrivateKey.Public.RegionID` 存的是 `-1` 时代表「每次启动重挑」（tailcat.go:167-169），只适合一次性场景。
- README.md:425-426 承认 TODO：DERP map 变化时客户端不够鲁棒（issue #7）。自建 DERP 并用长地址可以绕开这个问题。

### C.4 客户端身份 key 在库里如何表示

- 类型就是 `tailscale.com/types/key.NodePrivate`，`Client.Key` 字段（tailcat.go:1510）。为零值时首次使用生成临时 key（`nodeKeyLocked`，tailcat.go:1543-1552）。
- 公钥用 `Client.PublicKey()` 取（tailcat.go:1672-1676），返回 `key.NodePublic`，`.String()` 是 `nodekey:<hex>` 文本格式（README.md:383）。
- `genkey --client` 落盘的也是 `tailcat.PrivateKey` JSON，但 `Public.RegionID` 为 0、`Region` 为空（cmd/tailcat/tailcat.go:1627-1640）：客户端 key 不需要区域。CLI 读回时只取 `conf.Private`（cmd/tailcat/tailcat.go:739-743）。
- 客户端 disco key 同样由 `discoPrivateForNode(priv)` 派生（`createEngine` 里 `conf.ForceDiscoKey`，tailcat.go:1483），meow ping 包里带上 `(nodeKey, discoKey)`（disco.go:32-39）。

### C.5 `PrivateKey` 类型与 JSON 文件格式

```go
// tailcat.go:226-229
type PrivateKey struct {
    Private key.NodePrivate
    Public  ConnInfo
}
func NewPrivateKey() *PrivateKey   // tailcat.go:234-241：生成 key，填好 ServerPublic 和 ServerDiscoPublic，Region 留给调用方
```

CLI 用 `json.MarshalIndent(priv, "", "\t")` 直接序列化（cmd/tailcat/tailcat.go:1709），`key.NodePrivate` 实现了 `MarshalText`，所以文件形如：

```json
{
	"Private": "privkey:<64 hex>",
	"Public": {
		"ServerPublic": "nodekey:<64 hex>",
		"ServerDiscoPublic": "discokey:<64 hex>",
		"RegionID": 302
	}
}
```

（`Region` 有 `omitempty`，自建 DERP 时会出现 `"Region": [{"Nodes":[{"HostName":"derp.example.com"}]}]`。）

存放路径：`os.UserConfigDir()/tailcat/keys/<name>.private.json`，权限 `0600`（cmd/tailcat/tailcat.go:1529-1538, 1714）。魔法名字：服务端 `default`、客户端 `client-default` 自动加载（cmd/tailcat/tailcat.go:1155-1163, 722-730）。

**嵌入库时不必用这个文件格式**，`Server.Key` / `Client.Key` 只要一个 `key.NodePrivate`；我们可以存在自己的配置/数据库里。但沿用这个 JSON 格式有一个好处：与 tailcat CLI 互操作（排障时能用 `tailcat --key=/path/xxx.private.json` 直接起同一身份）。

### C.6 服务端如何只允许特定客户端 nodekey

两条路径，效果相同（都写 `lb.allowedClients` map，tailcat.go:272）：

1. **启动前**：`Server.AllowedClients []key.NodePublic`（tailcat.go:343-347），`Start()` 时灌入（tailcat.go:430-432）。
2. **运行时**：`Server.AddAllowedClient(k key.NodePublic)`（tailcat.go:679-692）。未 Start 时追加到切片；已 Start 时加锁写 map。

语义细节（都有代码证据）：

- **map 为 nil = 允许所有人**；**第一次 Add 之后就变成白名单模式**（`onMeow`，tailcat.go:1367-1370：`b.allowedClients != nil && !b.allowedClients[src]` → 忽略）。这意味着「先 allow-all 配对，配对成功后 Add 第一个 key」会立刻把其他人锁在门外。
- 被拒的客户端**收不到任何回应**（meowed 不发，tailcat.go:465-468），它的 `Client.Ping` 会在 10s 后 `context.DeadlineExceeded`（tailcat.go:1769；测试 tailcat_test.go:88-91 验证了这点）。攻击者无法探测服务端是否存在。
- CLI 的 `--allow=none` 就是 `AddAllowedClient(key.NodePublic{})`（加一个零值 key 让 map 非 nil，cmd/tailcat/tailcat.go:1229-1231）。库里同理可以做「先关门再逐个放行」。
- **没有 `RemoveAllowedClient`**，也没有列举接口。要撤销一个客户端只能重启 Server（或自己包一层，在 `OnTCP` handler 里再做一次应用层校验并拒绝）。
- **已经 meow 成功的客户端会留在 `lb.clients` map 里，永不驱逐**（`onMeow` 只增不减，tailcat.go:1372-1387）。白名单只在 meow 时检查，所以后加入白名单之前就已经连上的客户端不受影响；反过来也说明无法「踢掉」在线客户端。

### C.7 从入站连接反查客户端 nodekey

`OnTCP` 的 handler 只拿到 `net.Conn`，`RemoteAddr()` 是客户端的隧道内 IPv6（公钥前 10 字节派生），**不可逆**。但 `Server.Status()`（tailcat.go:1951-1953）返回 `*ipnstate.Status`，其 `Peer` map 以 `key.NodePublic` 为键、值里有 `TailscaleIPs`（上游 ipnstate 类型），可以把 `conn.RemoteAddr()` 的 IP 反查到完整公钥。配对协议里建议同时在应用层让客户端自报公钥，再用 `Status()` 交叉验证。

---

## B. 最小可运行嵌入示例

两段代码都基于当前 API（tailcat.go）编写；类型签名核对过，未实际编译运行（只读任务），但每个调用点旁标了行号。

### B.1 被控机 agent：固定 key 起 listener，入站转发到本机 `127.0.0.1:22` / unix socket

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
	"tailscale.com/wgengine/filter"
)

// 我们自己定义的端口约定（隧道内端口，与本机端口无关）：
const (
	portSSH    = 22   // 转发到本机 sshd
	portHerdr  = 7000 // 转发到 herdr 的 unix socket
	portCtrl   = 7001 // 配对 / 心跳 / 元数据用的自有协议
)

// loadOrCreateKey 用 tailcat CLI 相同的 JSON 格式持久化（C.5），便于用 CLI 排障。
// 第一次生成时把 DERP region 固定下来（C.3），之后每次启动都用同一区域，地址不变。
func loadOrCreateKey(path string, derp *tailcfg.DERPRegion) (*tailcat.PrivateKey, error) {
	if b, err := os.ReadFile(path); err == nil {
		pk := new(tailcat.PrivateKey)
		return pk, json.Unmarshal(b, pk)
	}
	pk := tailcat.NewPrivateKey()          // tailcat.go:234
	pk.Public.Region = []*tailcfg.DERPRegion{derp} // 自建 DERP：直接嵌主机名，地址天然是长地址
	b, _ := json.MarshalIndent(pk, "", "\t")
	os.MkdirAll(filepath.Dir(path), 0700)
	return pk, os.WriteFile(path, b, 0600)
}

func forwardTo(network, addr string, logf logger.Logf) func(net.Conn) {
	return func(c net.Conn) {
		local, err := net.DialTimeout(network, addr, 5*time.Second)
		if err != nil {
			logf("dial %s %s: %v", network, addr, err)
			c.Close()
			return
		}
		tailcat.ProxyConns(c, local) // tailcat.go:1931：双向拷贝 + 半关闭传播
	}
}

func main() {
	logf := log.Printf // 生产可换 logger.Discard 或接到我们的日志系统

	// 自建 DERP（G 节）。只给 HostName 时 DERP 走 443、STUN 走 3478（tailcfg 默认）。
	derp := &tailcfg.DERPRegion{
		RegionID: 1, RegionCode: "herdr",
		Nodes: []*tailcfg.DERPNode{{Name: "herdr-derp", RegionID: 1, HostName: "derp.example.com"}},
	}
	pk, err := loadOrCreateKey(filepath.Join(os.Getenv("HOME"), ".config/herdr-agent/node.private.json"), derp)
	if err != nil {
		log.Fatal(err)
	}

	// 白名单：安装时注入的「我们 Go 服务端」的客户端公钥（H 节讨论配对方案）。
	var allowed []key.NodePublic
	for _, s := range os.Args[1:] { // e.g. nodekey:cfb6bf...
		var k key.NodePublic
		if err := k.UnmarshalText([]byte(s)); err != nil {
			log.Fatalf("bad --allow key %q: %v", s, err)
		}
		allowed = append(allowed, k)
	}

	s := &tailcat.Server{
		Key:            pk.Private,           // tailcat.go:319
		Logf:           logf,                 // tailcat.go:323
		Region:         pk.Public.Region[0],  // tailcat.go:327：给了 Region 就不拉 DERP map
		AllowedClients: allowed,              // tailcat.go:347；为空 = 放行所有人
		// 包过滤器只放行这三个端口的 SYN（tailcat.go:377-388），其它端口静默丢弃。
		ServedTCPPorts: []filter.PortRange{
			{First: portSSH, Last: portSSH},
			{First: portHerdr, Last: portCtrl},
		},
	}
	s.OnTCP = func(port uint16) func(net.Conn) { // tailcat.go:363
		switch port {
		case portSSH:
			return forwardTo("tcp", "127.0.0.1:22", logf)
		case portHerdr:
			return forwardTo("unix", os.ExpandEnv("$XDG_RUNTIME_DIR/herdr.sock"), logf)
		case portCtrl:
			return handleControl(s) // 我们自己的配对/心跳协议，见 H 节
		}
		return nil // 回 RST
	}
	if err := s.Start(); err != nil { // tailcat.go:395：连 DERP、起 WireGuard + netstack
		log.Fatal(err)
	}
	defer s.Close()

	// 长地址（嵌入 DERP 节点，客户端零查询）。二维码里放这个。
	fmt.Println("tailcat addr:", s.TailcatAddr()) // tailcat.go:698
	// 运行期追加白名单示例（配对成功后调用）：
	// s.AddAllowedClient(peerKey)            // tailcat.go:683
	select {}
}

func handleControl(s *tailcat.Server) func(net.Conn) {
	return func(c net.Conn) {
		defer c.Close()
		// 1. 读客户端自报的 nodekey + 一次性 token
		// 2. 用 s.Status().Peer 把 c.RemoteAddr() 反查到真实 nodekey（C.7），与自报值比对
		// 3. token 正确 → s.AddAllowedClient(nodekey)，持久化到本地，回 OK
		_ = context.Background()
	}
}
```

要点：

- `OnTCP` 是「按端口返回 handler」的工厂，**每条入站 TCP 流调一次**（netstack `GetTCPHandlerForFlow`，tailcat.go:488-494）。返回 `nil` 发 RST。
- handler 里拿到的 `net.Conn` 是 gVisor 的 `*gonet.TCPConn`，支持 `CloseWrite()` 半关闭（tailcat.go:1918-1920 的 `closeWriter`）。
- 转发到 unix socket 完全可行：handler 是任意 Go 函数，`net.Dial("unix", …)` 即可。CLI 的 `serve 22` 只能转 `localhost:<port>`（cmd/tailcat/tailcat.go:1241-1251, 1309），这是自写二进制的一个明确优势。
- 进程退出前如果刚 `Close()` 过连接，要 `s.DrainTCP(ctx)`（tailcat.go:584-617），否则 FIN 可能没发出去。长驻 agent 一般不需要。

### B.2 我们的 Go 服务端：用固定 client key 拨号，拿到 `net.Conn`

```go
package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"os"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

// 服务端的客户端身份：一个 key.NodePrivate，持久化在我们自己的 secret 存储里。
// 公钥 (priv.Public().String() => "nodekey:...") 要提前交给 agent 放进白名单。
func loadClientKey(path string) key.NodePrivate {
	if b, err := os.ReadFile(path); err == nil {
		var pk tailcat.PrivateKey // 兼容 `tailcat genkey --client` 的文件
		if json.Unmarshal(b, &pk) == nil && !pk.Private.IsZero() {
			return pk.Private
		}
	}
	priv := key.NewNode()
	pk := tailcat.PrivateKey{Private: priv}
	pk.Public.ServerPublic = tailcat.NodePublic{NodePublic: priv.Public()}
	pk.Public.ServerDiscoPublic = tailcat.DiscoPublicForNode(priv) // tailcat.go:1974
	b, _ := json.MarshalIndent(pk, "", "\t")
	os.WriteFile(path, b, 0600)
	return priv
}

// AgentConn 是「一个被控机」对应的一条 tailcat Client（= 一个 WireGuard 引擎 + 一个 netstack）。
type AgentConn struct {
	cl *tailcat.Client
}

func DialAgent(ctx context.Context, addr tailcat.Addr, clientKey key.NodePrivate, logf logger.Logf) (*AgentConn, error) {
	cl := &tailcat.Client{
		Server: addr,        // tailcat.go:1505：agent 的 tailcat 地址（二维码里拿到的）
		Key:    clientKey,   // tailcat.go:1510：固定身份，agent 白名单认这个
		Logf:   logf,
		// DERPMapURL / DERPMapCache 只在地址是短地址时才用到；长地址下不发任何 HTTP 请求
	}
	// Ping = 懒启动网络栈 + meow/meowed 握手（tailcat.go:1746-1764）。
	// 内部硬编码 10s 超时，每 1s 重发（tailcat.go:1769, 1799）。
	// 被白名单拒绝的表现就是这里 DeadlineExceeded（服务端不回 meowed）。
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := cl.Ping(pctx); err != nil {
		cl.Close()
		return nil, err
	}
	return &AgentConn{cl: cl}, nil
}

// OpenSSH 在隧道里开一条到 agent 22 端口的 TCP 流，返回普通 net.Conn，
// 之后直接交给 golang.org/x/crypto/ssh 的 NewClientConn 即可（cmd/tailcat/ls.go:69-82 就是这么用的）。
func (a *AgentConn) OpenSSH(ctx context.Context) (net.Conn, error) {
	return a.cl.DialTCPPort(ctx, 22) // tailcat.go:1881
}

// OpenHerdr 开一条到 agent 上 herdr unix socket 的流（agent 侧 OnTCP(7000) 转发）。
func (a *AgentConn) OpenHerdr(ctx context.Context) (net.Conn, error) {
	return a.cl.DialTCPPort(ctx, 7000)
}

// Healthy 用 disco ping 探活，并顺带推动直连升级（tailcat.go:1816-1863）。
// 注意不要用 cl.Ping 做探活：首次成功后 meowWait 已关闭，之后 Ping 立即返回成功（D 节）。
func (a *AgentConn) Healthy(ctx context.Context) (direct bool, err error) {
	r, err := a.cl.DiscoPing(ctx)
	if err != nil {
		return false, err
	}
	return r.Endpoint != "", nil
}

func (a *AgentConn) Close() error { return a.cl.Close() }

func main() {
	clientKey := loadClientKey("/var/lib/herdr-server/client.private.json")
	log.Println("our client nodekey (put in agent allowlist):", clientKey.Public().String())

	ctx := context.Background()
	agent, err := DialAgent(ctx, tailcat.Addr(os.Args[1]), clientKey, log.Printf)
	if err != nil {
		log.Fatal(err)
	}
	defer agent.Close()

	c, err := agent.OpenSSH(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()
	// c 是 net.Conn，直接给 x/crypto/ssh 或者 io.Copy 到 WebSocket
}
```

### B.3 key 持久化与 `--allow` 白名单的库层对应关系

| CLI | 库 |
|---|---|
| `tailcat genkey --key=default --region=derp.example.com` | `tailcat.NewPrivateKey()` + 手填 `Public.Region`，`json.Marshal` 落盘 |
| `tailcat genkey --key=default --fixed-region` | `FetchDERPMap` + `PickBestRegion` 得 ID，写入 `Public.RegionID`（cmd/tailcat/tailcat.go:1664-1682） |
| `tailcat genkey --client --key=client-default` | `key.NewNode()`，公钥 `.Public().String()` |
| `tailcat serve --allow=nodekey:a,nodekey:b 22` | `Server.AllowedClients = []key.NodePublic{a, b}` 或多次 `AddAllowedClient` |
| `tailcat serve --allow=none` | `AddAllowedClient(key.NodePublic{})`（先关门） |
| `tailcat serve --full-address` | `Server.TailcatAddr()` 本身就是长地址 |
| `tailcat serve 8080,22` | `ServedTCPPorts` + `OnTCP` 按端口分发 |
| `~/.cache/tailcat/derpmap-*.json` 磁盘缓存 | 实现 `tailcat.DERPMapCache` 接口（tailcat.go:112-122）赋给 `Server.DERPMapCache` / `Client.DERPMapCache`；长地址下用不到 |

---

## A. 公开 Go API 全貌

包路径 `github.com/tailscale/tailcat`。**官方声明无 API 稳定性承诺**（tailcat.go:27-30，README.md:621-629）。下表是 `grep '^(func|type|var|const) [A-Z]'` 的完整结果（非测试文件），逐项核对过。

### A.1 服务端

| 符号 | 签名 | 一句话 | 证据 |
|---|---|---|---|
| `Server` | `struct` | 监听方。零值可用，`Start()` 补默认值 | tailcat.go:316-389 |
| `Server.Key` | `key.NodePrivate` | 节点身份；零值 = 每次生成临时 key | :319 |
| `Server.Logf` | `logger.Logf` | 日志钩子；nil = `log.Printf`；静音用 `logger.Discard` | :323 |
| `Server.Region` | `*tailcfg.DERPRegion` | 直接指定 DERP 区域，**不拉 map**（自建 DERP 走这里） | :327 |
| `Server.RegionID` | `tailcfg.DERPRegionID` | 从 map 里取该 ID；0 = 启动时 netcheck 挑最近 | :332 |
| `Server.DERPMapURL` | `string` | 替代 `DefaultDERPMapURL` | :337 |
| `Server.DERPMapCache` | `DERPMapCache` | map 缓存实现；nil = 进程内内存缓存 | :341 |
| `Server.AllowedClients` | `[]key.NodePublic` | 启动时白名单；空 = 放行所有 | :347 |
| `Server.AllowProxy` | `func(netip.AddrPort) bool` | 字段存在但**当前代码没有任何地方读取它**（死字段） | :353 |
| `Server.OnTCP` | `func(port uint16) func(net.Conn)` | 到服务端自身地址的入站 TCP，按端口返回 handler；nil → RST | :363 |
| `Server.OnTCPForward` | `func(netip.AddrPort) func(net.Conn)` | exit-node 模式：到任意目标的入站 TCP；设置后过滤器放开全部目的地 | :375 |
| `Server.ServedTCPPorts` | `[]filter.PortRange` | 包过滤层只放行这些端口的 SYN；被滤掉的连接**静默超时**而非 RST | :388 |
| `(*Server).Start()` | `error` | 拉/选 DERP 区域 → 建 eventbus/netmon/dialer/engine/netstack → 装过滤器 → 连 DERP | :395-530 |
| `(*Server).TailcatAddr()` | `Addr` | 返回**嵌入 DERP 区域的长地址**；须在 Start 后 | :698 |
| `(*Server).Addr()` | `netip.Addr` | 服务端隧道内 IPv6（由公钥派生）；须在 Start 后 | :574 |
| `(*Server).AddAllowedClient(k)` | | 运行时追加白名单；首次调用即从 allow-all 切到白名单 | :683-692 |
| `(*Server).Status()` | `*ipnstate.Status` | WireGuard/DERP 状态；`Peer` map 键为客户端 `key.NodePublic`，值含 `TailscaleIPs`/`CurAddr`/`Relay`/`LastHandshake`/`RxBytes`（上游 ipnstate.go:237-276） | :1951 |
| `(*Server).DrainTCP(ctx)` | `error` | 等 netstack 内所有 TCP 端点完全关闭（进程退出前用） | :599-617 |
| `(*Server).Close()` | `error` | 关 netstack、engine、netmon、dialer、bus；会中断活跃连接（tailcat_test.go:222-285） | :577-582 |
| `(*Server).SSHConnHandler(opts SSHOptions)` | `func(net.Conn)` | 内置免认证 SSH 服务器（gliderssh）：shell/exec/PTY + SFTP 子系统；只注册 `session` channel，**没有** direct-tcpip 端口转发 | tailcat_ssh.go:50-81 |
| `(*Server).HandleTailscaleSSHConn(c)` | | `SSHConnHandler(SSHOptions{Shell:true})(c)` 的快捷方式 | tailcat_ssh.go:35 |
| `SupportsSSHServer()` | `bool` | linux/darwin/windows 且未加 `ts_omit_ssh` 时为 true | tailcat_ssh.go:31, tailcat_ssh_stub.go:14 |
| `SSHOptions{Shell bool; Files *FileService}` | | 内置 SSH 的能力开关 | tailcat_files.go:47-57 |
| `FileService{Dir string; Mode FileServeMode}` / `FileServeRO/RW/WO/WOPlus` | | `os.Root` 限定目录的 SFTP 服务 | tailcat_files.go:11-43 |

### A.2 客户端

| 符号 | 签名 | 一句话 | 证据 |
|---|---|---|---|
| `Client` | `struct` | 拨号方。只需 `Server` 字段；网络栈在首次 Dial/Ping 时懒建 | tailcat.go:1502-1538 |
| `Client.Server` | `Addr` | 目标 tailcat 地址（必填） | :1505 |
| `Client.Key` | `key.NodePrivate` | 客户端身份；零值 = 首次使用生成临时 key | :1510 |
| `Client.Logf` / `DERPMapURL` / `DERPMapCache` | | 同 Server | :1514-1525 |
| `NewClient(server Addr)` | `*Client` | `&Client{Server: server}` | :1558 |
| `(*Client).PublicKey()` | `key.NodePublic` | 取（必要时先生成）客户端公钥，用于白名单 | :1672 |
| `(*Client).Ping(ctx)` | `(PingResult, error)` | 懒启动 + meow/meowed 握手；内部固定 10s 超时，1s 重发；**首次成功后再调用立即返回**（见 D） | :1754-1814 |
| `(*Client).DiscoPing(ctx)` | `(*ipnstate.PingResult, error)` | 真正的 disco ping，`Endpoint != ""` 表示直连；同时触发直连升级；无内部超时，靠 ctx | :1824-1863 |
| `(*Client).Dial(ctx, network, addr)` | `(net.Conn, error)` | 通用拨号（经 tsdial.UserDial） | :1872 |
| `(*Client).DialTCPPort(ctx, port)` | `(net.Conn, error)` | 拨服务端自身某端口（最常用） | :1881 |
| `(*Client).DialTCP(ctx, ap)` | `(net.Conn, error)` | 经服务端 exit-node 拨任意 IP:port；IPv4 走 NAT64 前缀 `64:ff9b::/96` | :1898-1909 |
| `(*Client).DrainTCP(ctx)` | `error` | 等发送队列排空（进程退出前用） | :629-665 |
| `(*Client).Close()` | `error` | 关闭整个网络栈 | :1679 |
| `PingResult{Latency time.Duration}` | | DERP 往返（meow→meowed） | :1689-1693 |

### A.3 地址 / 密钥 / DERP map

| 符号 | 签名 | 一句话 | 证据 |
|---|---|---|---|
| `Addr` | `string` | `tc` + base64url(CBOR(ConnInfo)) | :139 |
| `ConnInfo{ServerPublic, ServerDiscoPublic, Region, RegionID}` | | 地址的解码形式 | :145-170 |
| `(*ConnInfo).Addr()` | `Addr` | 编码；会剥掉 RegionID/RegionCode/RegionName/冗余 Name 以缩短 | :744-778 |
| `ParseAddr(a)` | `(ConnInfo, error)` | 解码并补回被剥字段；拒绝 CBOR null | :836-882 |
| `ParseAddrRaw(a)` | `(any, error)` | 只解 wire 形式，用于 JSON 展示（`tailcat parse`） | :829 |
| `(Addr).Resolve(ctx, opts...)` | `(Addr, error)` | 短地址 → 长地址（拉 map，保留 ≤2 个节点） | :787-804 |
| `(*ConnInfo).Expand(ctx, opts...)` | `error` | 用 map 把 RegionID 展开成 Region；`-1` 触发 netcheck 选区 | :1036-1116 |
| `FetchDERPMap(ctx, opts...)` | `(*tailcfg.DERPMap, error)` | 拉 map（带 ETag/1h 新鲜度缓存，8MB 上限，10s 超时） | :892-909, 950-1008 |
| `PickBestRegion(ctx, dm)` | `(tailcfg.DERPRegionID, error)` | netcheck 选延迟最低区域；js 构建下永远返回 0 | pickregion.go:24, pickregion_js.go:19 |
| Expand/Fetch 的 option 类型 | `DERPMapURL(string)` / `*tailcfg.DERPMap` / `ExpandForServer` / `DERPMapCache` | 用 `...any` 传入 | :98-134 |
| `DERPMapCache` 接口 | `Get(url) (data, etag, storedAt, ok)`; `Put(url, data, etag) error` | 自定义缓存；CLI 的磁盘实现见 cmd/tailcat/tailcat.go:762-810 | :112-122 |
| `DefaultDERPMapURL` | `"https://tailcat.dev/derpmap.json"` | | :96 |
| `PrivateKey{Private key.NodePrivate; Public ConnInfo}` / `NewPrivateKey()` | | 见 C.5 | :226-241 |
| `NodePublic{key.NodePublic}` / `DiscoPublic{key.DiscoPublic}` | 带 `MarshalBinary`/`Equal` | CBOR 用 32 字节裸编码的包装 | :174-221 |
| `DiscoPublicForNode(k key.NodePrivate)` | `DiscoPublic` | 由节点私钥派生 disco 公钥；手工构造 `ConnInfo` 时必须填 | :1974 |
| `Verbose` | `var bool` | netcheck 的额外日志开关（全局变量） | :91 |
| `README` | `var string` | 嵌入的 README.md | readme.go:13 |

### A.4 工具与底层协议

| 符号 | 一句话 | 证据 |
|---|---|---|
| `ProxyConns(a, b net.Conn)` | 双向拷贝，一方 EOF 时对另一方 `CloseWrite` 半关闭，两边都完再 Close | tailcat.go:1931-1948 |
| `IsMeowPacket` / `IsMeowedPacket` / `EncodeMeowPing` / `EncodeMeowed` / `ParseMeowPing` | meow 握手包编解码（`"meow"` 魔数 + 类型字节 + 32B nodekey + 32B discokey） | disco.go:26-70 |
| `ConnBlob` / `(*Server).ConnBlob()` / `(*ConnInfo).ConnBlob()` / `ParseConnBlob[Raw]` | 旧名字的 Deprecated 别名 | connblob_deprecated.go |

### A.5 API 里**没有**的东西（需要自己做）

- 没有 `RemoveAllowedClient` / 列举白名单 / 踢掉在线 peer。
- 没有「重连」或「重新握手」接口（见 D）。
- 没有从 `net.Conn` 直接取对端 nodekey 的方法（要走 `Status().Peer` 反查，C.7）。
- 没有 UDP：`NetstackDialUDP` 直接 `panic("unreachable from tailcat")`（tailcat.go:517, 1659），包过滤器只放 TCP（tailcat.go:552-556）。
- 没有服务端 → 客户端方向的拨号：客户端过滤器拒绝所有入站 SYN（tailcat.go:1636-1650）。
- 没有 Listener 抽象（`net.Listener`）；入站只有回调式 `OnTCP`。

---

## D. 连接建立过程、耗时与断线重连语义

### D.1 时序（ASCII）

```
 被控机 agent (tailcat.Server)          DERP relay (TLS/443, STUN/3478 udp)          我们的 Go 服务端 (tailcat.Client)
 ─────────────────────────────          ────────────────────────────────────          ────────────────────────────────
 Start():
  Region 已给 → 不拉 map (tailcat.go:407-421)
  createEngine + netstack (477-507)
  lb.Start(): mc.SetDERPMap / SetNetworkMap
  magicsock 连 home DERP ──TLS──────────▶ [derp conn: server pubkey]
  (0.1–0.5s, 取决于到 DERP 的 RTT)
                                                                                     首次 Dial/Ping:
                                                                                      ensureStarted(): ParseAddr; 长地址 → Expand 无网络 (1701-1731)
                                                                                      短地址 → HTTPS 拉 derpmap (10s 超时, 950-1008)
                                                                                      RegionID=-1 → netcheck 探测 (可再花 1–3s, cmd 注释 1484-1486)
                                                                                      lb.Start(): 连同一个 DERP 区域 ──TLS──▶
                                                                                      ping(): 每 1s 重发 meow, 上限 10s (1769,1799)
  onDERPRecv ◀───── "meow"+nodekey+discokey (disco.go:32) ◀── DERP ◀───────────────── SendDERPPacketTo(serverPub, region) (1789)
  onMeow(): 白名单检查 (1367) → 加 peer 到 clients/netmap (1377-1413)
  ────── "meowed" ──▶ DERP ──▶ ────────────────────────────────────────────────────▶ onMeowed: close(meowWait) (1609-1614)
  go advertiseEndpoints()  (1421)                                                   lb.advertiseEndpoints() (1613)
  ────── disco CallMeMaybe{我的 UDP 端点} ──▶ DERP ──▶ ─────────────────────────────▶
  ◀───── disco CallMeMaybe{它的 UDP 端点} ◀── DERP ◀─────────────────────────────────
  [magicsock 双方互发 disco ping 到对方端点, UDP 打洞]  (上游 magicsock 行为, README.md:594-601)
                                                                                     DialTCPPort(22): netstack SYN → WireGuard
  ◀═══ WireGuard handshake (init/resp) 先经 DERP ═══▶                                 (wireguard-go 对未知 peer 懒建, 1331-1336)
  ◀═══ 加密的 TCP SYN/SYN-ACK/ACK ═══▶  (此时还在 DERP 上)
  GetTCPHandlerForFlow → OnTCP(22) → handler(conn)  (488-494)
  [直连打洞成功 → magicsock 把后续包切到 UDP 直连；失败则一直走 DERP]
```

### D.2 首次连接典型耗时（估算，基于代码里的超时和 README 描述）

| 阶段 | 典型 | 备注 |
|---|---|---|
| Client 建栈（无网络） | 数十 ms | `initLocked`，tailcat.go:1566-1668 |
| 短地址：拉 DERP map | 0.1–1s（首次），命中缓存 0 | 长地址跳过。10s 超时后有陈旧缓存则用之（tailcat.go:965-975） |
| `RegionID=-1` 自动选区 netcheck | 1–3s | 只在服务端 Start 且未给 Region/RegionID 时发生；客户端不会（地址里必有区域） |
| 连 DERP（TLS 握手） | 1 RTT + TLS ≈ 100–500ms | 两边各自 |
| meow/meowed | 1 DERP 往返（几十到几百 ms） | 若某一边 DERP 还没连上，第一发丢包，1s 后重发（tailcat.go:1780-1787） |
| WireGuard 握手 + TCP 三次握手（经 DERP） | 2–3 个 DERP 往返 | 首条流 |
| **首字节合计（经 DERP）** | **约 0.5–2s**（长地址）；短地址再加 map 拉取 | |
| 直连升级 | 通常几秒内 | `ping --until-direct` 默认给 10s（cmd/tailcat/tailcat.go:105），测试给 30s（ping_test.go:32） |

代码里的现成超时预算可作参考：客户端 Dial 10s（cmd/tailcat/tailcat.go:877）、SOCKS 拨号 15s 并特别注释「5s 会被一次 WireGuard 握手重传吃掉」（cmd/tailcat/tailcat.go:994-1000）、wasm 端 60s 总预算（web/main_js.go:167）。建议我们：Ping 15s、单流 Dial 15s。

### D.3 断线重连：库层面有什么、没有什么

**有（都是上游 magicsock/wireguard-go 行为，tailcat 直接继承）：**

- **DERP 连接断了会自动重连**：`runDerpReader` 收包出错 → 标记 region 断开 → `ReSTUN` → backoff（上限 5s）→ 重连（上游 wgengine/magicsock/derp.go:558-598）。
- **WireGuard 会话过期自动重握手**（WireGuard 协议：约 2 分钟 rekey，握手重传 5s）。
- **本机 UDP 端点变化会重新广播**：`onEngineStatus` 发现 `LocalAddrs` 变了就 `advertiseEndpoints`（tailcat.go:1186-1205, 1207-1249），双方仍互认时可以在换网后重新打洞。
- **直连失败自动回落 DERP**（README.md:598-601）。
- **客户端 `ensureStarted` 失败可重试**：`started` 为 false 时下一次 Dial 会再试（tailcat.go:1695-1731）。

**没有（必须我们自己做）：**

1. **对端进程重启后，另一侧的对象作废。** 服务端的 `clients` map 在内存里（tailcat.go:270），agent 重启后不再认识我们的 Client；而 Client 侧 `upDone` 已置 true、`meowWait` 已 close，`Ping()` 会**立即返回成功**（`select` 命中已关闭的 channel，tailcat.go:1801-1804），永远不会再发 meow。后续 `Dial` 会因为服务端 `peerAllowedIPs` 返回 `ok=false` 拒绝未知 peer 而超时。**结论：任何一侧重启，另一侧必须 `Close()` 旧 Client 并 new 一个新的。** 这也是 CLI 每次运行都是新进程所以不会遇到的问题。
2. **没有连接状态回调。** 要知道「断了」只能：Dial 超时 / 已有流读写出错 / 周期 `DiscoPing` 失败 / `Status().Peer[k].LastHandshake` 太旧。
3. **`Ping()` 不能当探活用**（同第 1 点）。探活用 `DiscoPing`（tailcat.go:1824-1863，真实往返，还顺带推动直连升级）。
4. **服务端不会驱逐死客户端**，`clients`/netmap 只增不减（tailcat.go:1372-1387）。对我们的场景（每台被控机只有很少几个固定 client key）无影响；如果客户端每次用临时 key，长期运行会慢慢积累 peer。

建议的监督循环（Go 服务端侧，每个 agent 一个）：

```
loop:
  cl = new Client{Server: addr, Key: ourKey}
  if cl.Ping(15s) 失败 → 指数退避后 goto loop
  每 30s: cl.DiscoPing(10s)；连续 2 次失败 → cl.Close(); goto loop
  业务 Dial 失败超过阈值 → 同上
```

### D.4 多客户端 / 多流

- **一个 listener 可同时被多个客户端连接**：`clients map[key.NodePublic]*tailcfg.Node`，ID 从 2 递增（tailcat.go:270, 1375），每个客户端有自己由公钥派生的 IPv6 和 WireGuard peer。测试里也是多 client 并发（cmd/tailcat/socks_test.go 用多个 Client）。
- **每个客户端可开任意多条 TCP 流**：TCP 由 gVisor netstack 终结（README.md:562-565），一个 WireGuard 会话承载所有流；`OnTCP` 每条流回调一次。**不是每条流一个 WireGuard 会话**。
- **同一个 client key 不能被两个进程同时使用**去连同一个服务端：两者派生出同一个 IPv6、同一个 peer 记录，WireGuard 会话会互相覆盖。我们如果有多个服务端副本，每个副本需要独立的 client key（H 节讨论）。

---

## E. 单隧道承载多个 TCP 端口 / 多路复用

**结论：天然支持，而且零成本。** tailcat 的「端口」是隧道内 IPv6 地址上的真实 TCP 端口（gVisor netstack），不是应用层多路复用，所以：

- `tailcat serve 8080,22` 在 CLI 里的实现只是 `portSet` + `OnTCP` 里 `portSet.Contains(port)` 判断然后 `tcpForwardTo("localhost:<port>")`（cmd/tailcat/tailcat.go:1273-1310）；再加 `ServedTCPPorts = portRanges(ports)` 收紧包过滤（cmd/tailcat/tailcat.go:1220-1226, 1458-1467）。
- 库层区分目标端口的唯一入口就是 `OnTCP(port uint16)` 的参数（tailcat.go:363, 488-494）。端口空间 1–65535 随便定义，与本机端口无关（B.1 示例把 7000 映射到 unix socket）。
- 客户端 `DialTCPPort(ctx, port)` 拨 `[serverIPv6]:port`（tailcat.go:1881-1886）。
- `exit-node` 模式（`OnTCPForward`）还能让客户端拨 `任意 IP:port`，IPv4 走 NAT64 前缀（tailcat.go:1893-1909, 498-504），CLI 用它做 SOCKS5（cmd/tailcat/tailcat.go:991-1016）。对 herdr 场景不需要开这个，开了等于把被控机变成出口节点，安全面大很多。
- 多路复用层级：**IP 层**（一个 WireGuard peer 会话 ↔ 多条 TCP 流），比 SSH channel/yamux 之类应用层复用更省一层。但每条隧道内 TCP 流有 netstack 的独立拥塞控制/重传，走 DERP 时所有流共享 DERP 的限速。
- `forward` 子命令是「本地 TCP 监听 → 每个 accept 一次 `DialTCPPort`」（cmd/tailcat/forward.go:99-126），就是多流的活例子。
- **UDP 不支持**（A.5）。herdr 是 SSH/unix socket，无影响。
- **反向方向不支持**：服务端不能主动向客户端发起 TCP（tailcat.go:1636-1650）。如果需要「agent 主动推事件到服务端」，要么 agent 也起一个 Server（第二个引擎），要么在一条 agent→服务端方向的长连接上跑对称复用协议（yamux/SSH），见 H.4 的反向拓扑。

---

## H. 部署形态建议：CLI + systemd vs 自写 Go 二进制；二维码内容

### H.1 两种形态对比

| 维度 | (1) `tailcat serve 22` + systemd | (2) 自写 Go 二进制嵌入 tailcat 库 |
|---|---|---|
| 转发目标 | 只能 `localhost:<同号端口>`（cmd/tailcat/tailcat.go:1309） | 任意：TCP、**unix socket**、进程内 handler |
| 白名单 | 启动参数 `--allow`，改动需重启（cmd/tailcat/tailcat.go:1227-1239） | `AddAllowedClient` 运行时追加 + 自己持久化 |
| 配对 / 二维码 | 无；只有 stderr 打印地址、`TAILCAT_ADDR_FILE` 落盘或推到 TCP（cmd/tailcat/tailcat.go:1337-1350） | 自己实现：显示二维码、一次性 token、与服务端的应用层握手 |
| 元数据（主机名、herdr 版本、在线状态） | 无 | 隧道内 ctrl 端口自有协议 |
| 断线/重启处理 | systemd `Restart=always` 即可（服务端是无状态监听方） | 同样简单；但要加健康上报 |
| 内置免认证 SSH | `serve no-auth-ssh`（gliderssh，只支持 session channel，无端口转发） | 可复用 `SSHConnHandler`，也可以直接转发到系统 sshd |
| 升级节奏 | 跟 tailcat 上游发版 | 我们自己控制 tailcat/tailscale.com 版本（I 节：两者必须一起升） |
| 体积 | 18 MB（release tags）；我们实测 | 同一数量级；我们自己的代码可忽略 |
| 二进制分发 | 官方 deb/rpm/brew/AUR/docker（README.md:44-104） | 我们自己发（goreleaser 配置可直接照抄 .goreleaser.yaml） |
| 上手成本 | 零代码 | 一个小 Go 程序（B.1 约 100 行 + 配对协议） |

**推荐 (2)。** 决定性因素有三个：unix socket 转发（herdr 大概率是 socket）、运行时白名单（配对流程需要）、隧道内 ctrl 通道（心跳、主机名、能力协商）。(1) 适合 PoC / 手动验证链路，一条命令就能跑通：`tailcat genkey --key=default --region=derp.example.com && tailcat serve --allow=nodekey:... 22`。

### H.2 拓扑 A（团队负责人假设）：agent = tailcat Server，服务端 = tailcat Client

```
[被控机 agent: Server]  ◀── WireGuard ──  [Go 服务端: 每台 agent 一个 Client 对象]  ◀── HTTPS/WS ──  [手机浏览器]
```

- 每台 agent 在服务端对应**一整套引擎**（eventbus + netmon + magicsock UDP socket + DERP 连接 + gVisor 栈），`Client` 只有一个 peer（tailcat.go:1124-1131 `serverPub` 是唯一 peer）。几十台无所谓；几百上千台时资源和 fd 线性增长，需要评估。
- 服务端多副本：每个副本必须有**独立 client key**（D.4），agent 白名单里要有所有副本的 key，或者做 agent→副本的粘性路由。
- agent 侧白名单从「安装时注入服务端公钥」起步最干净（B.1）：不存在 allow-all 窗口。

### H.3 拓扑 B（备选，为规模而设计）：服务端 = tailcat Server，agent = tailcat Client

```
[被控机 agent: Client]  ── WireGuard ──▶  [Go 服务端: 单个 Server 对象, clients map 容纳所有 agent]
   agent 拨 server:7000，建一条长 TCP 流，上面跑 yamux（对称复用）
   服务端要 SSH 到 agent 时：在该 yamux 会话里 Open 一条逻辑流 → agent 侧接到后 net.Dial("tcp","127.0.0.1:22") 或 unix socket
```

- **服务端只有一套引擎**，所有 agent 是同一个 Server 的 peer（tailcat.go:270, 1375 ID 递增），这是 tailcat 里唯一能「一对多」的对象。
- **配对 = 把 agent 的 nodekey 加进服务端白名单**：`AddAllowedClient(agentKey)`，天然运行时可加。撤销需要重启服务端（无 Remove），或在应用层拒绝该 key 的 yamux 会话。
- 服务端地址稳定：固定 key + 自建 DERP 长地址，写进 agent 安装脚本或 DNS TXT（README.md:358-366）。
- 代价：多一层 yamux；服务端重启后所有 agent 的 Client 作废，agent 需要重建 Client（D.3），这正好是 agent 本来就该有的重连循环。
- 直连打洞两边都会做（`advertiseEndpoints` 双向，tailcat.go:1421, 1613），与方向无关。

**建议：先按拓扑 A 做 MVP（与 tailcat 的心智模型一致、最少代码），但把「谁是 Server」抽象在一个接口后面，规模上来（>100 台在线）再切拓扑 B。** 两种拓扑的 agent 二进制都是自写 Go 程序，H.1 的结论不变。

### H.4 二维码里应该编码什么

被控机 agent 首次启动（或用户执行 `herdr-agent pair`）时在终端画二维码，手机扫码后打开我们的 Web 页面并把内容 POST 给 Go 服务端。建议 JSON（或 URL query）字段：

| 字段 | 内容 | 为什么 |
|---|---|---|
| `v` | 协议版本号 | tailcat wire 格式和我们的协议都可能变 |
| `addr` | **长地址**（`Server.TailcatAddr()`，嵌入自建 DERP 主机名） | 服务端拨号零查询、不依赖 tailcat.dev map（C.2、G） |
| `token` | 一次性配对 token（≥128 bit 随机，5–10 分钟过期） | 证明「扫码的人」拥有这台机器；agent 在 ctrl 端口校验 |
| `host` | 主机名 / 用户起的别名 | UI 展示 |
| `os`, `arch`, `agent_ver` | 元数据 | 兼容性与展示 |
| `svc` | （可选）我们服务端的 URL | 让二维码可被通用扫码器打开成一个 https 链接：`https://svc/pair#<base64 payload>` |

**不要放进二维码的：** agent 私钥（当然）、服务端 client key。拓扑 A 下服务端 key 在安装时已注入 agent；拓扑 B 下二维码里放的是 `agent_nodekey`（`priv.Public().String()`）而不是 `addr`。

长度估算：长地址约 180–220 字符 + 其它字段 ≈ 350 字节，QR byte mode 版本 15 以内，终端里渲染没问题（tailcat 地址是 base64url，含小写和 `-_`，必须用 byte mode，不能用 alphanumeric mode）。

配对时序（拓扑 A）：

```
agent: 生成 token，显示 QR{addr, token, host}
手机: 扫码 → POST /pair {payload} → Go 服务端
服务端: cl = Client{Server: addr, Key: ourKey}; cl.Ping()  // agent 白名单已含 ourKey → 成功
服务端: c = cl.DialTCPPort(7001); 发送 {ourNodeKey, token}
agent:  用 s.Status().Peer 反查 c.RemoteAddr() 得到真实 nodekey，与自报一致且 token 正确 → 回 OK，记录「已配对到账号 X」
服务端: 把 (agent addr, host, user) 写库；之后按需 DialTCPPort(22 / 7000)
```

如果安装时**不能**预置服务端公钥（例如一键安装脚本不想带参数），退化方案：agent 启动时白名单为空（allow-all），配对窗口 10 分钟；在 ctrl 端口验证 token 通过后立刻 `AddAllowedClient(peerKey)`（此刻起进入白名单模式，C.6），窗口到期仍未配对则 `AddAllowedClient(key.NodePublic{})` 关门。窗口期内地址只存在于终端二维码里，泄露面可控但不为零。

---

## G. DERP：默认 map 的限制、自建 derper 的成本，以及要不要放进我们的 Docker

### G.1 默认 map（tailcat.dev）

- URL：`https://tailcat.dev/derpmap.json`（tailcat.go:96），拉取时带 `Tailcat-Mode: server|client` 头（tailcat.go:981），服务端注释暗示 map 服务端可能按来源 IP 过滤区域（tailcat.go:1097-1100）。
- **官方措辞**：「free rate-limited」「no uptime SLAs or throughput targets」「we may revoke access to them at any time, for any reason」（README.md:35-36, 621-629）。走 DERP 中继时吞吐受限（README.md:600-601）。
- 直连成功后 DERP 只承载 disco 心跳，流量不经过它；但**打洞失败的用户（对称 NAT、企业网）会全程走 DERP 并吃到限速**。
- 依赖第三方的 map 还带来 C.3 提到的 issue #7：map 变化导致短地址失效。

### G.2 自建 derper 的成本与配置

上游 `tailscale.com/cmd/derper`（本机模块缓存里有源码，flags 见 cmd/derper/derper.go:57-92）：

| 项目 | 内容 |
|---|---|
| 安装 | `go install tailscale.com/cmd/derper@<与 tailcat 相同的 tailscale.com 版本>`；无官方包，README 要求自己构建并跟着升级 |
| 必需资源 | 一台有公网 IP 的机器；一个域名；入站 TCP 443（DERP over TLS）、TCP 80（ACME HTTP-01 + 明文回退）、**UDP 3478（STUN，打洞必需）** |
| 最小命令 | `derper -hostname derp.example.com -a :443 -http-port 80 -stun-port 3478 -certmode letsencrypt -certdir /var/lib/derper` |
| 加固 | `-disallow-app-names`（tailcat 用 `tailcat-server`/`tailcat-client` 作 app name，tailcat.go:1472-1475，可以反过来只放行这两个名字，但该 flag 是黑名单语义）；`-rate-config` JSON 限速；`-verify-clients` **不适用**（需要本机 tailscaled） |
| 资源占用 | 单二进制、几十 MB 内存；带宽 = 所有走中继的流量之和 |
| 在 tailcat 侧使用 | `Server.Region = &tailcfg.DERPRegion{RegionID:1, Nodes: []*tailcfg.DERPNode{{Name:"a", RegionID:1, HostName:"derp.example.com"}}}`；`STUNPort` 0 = 3478，`DERPPort` 0 = 443（上游 tailcfg/derpmap.go:229-243）。地址自动变长地址，客户端不需要任何 map |
| 多节点 | `-mesh-with` + `-mesh-psk-file` 做同区域 mesh；tailcat 一个地址最多带 2 个节点（tailcat.go:798-801），一个区域（tailcat.go:158-160） |

**另一条路：把 DERP 嵌进我们的 Go 服务端进程。** tailcat CLI 的 dev 模式就是这么做的：`derpserver.New(key.NewNode(), logf)` + `derpserver.Handler(d)` 挂在 HTTPS server 上，再起一个 20 行的 STUN 应答循环（cmd/tailcat/tailcat.go:1469-1523）。上游 `derp/derpserver` 是公开包（derpserver.go:375 `New`，handler.go:16 `Handler`）。这样「一个二进制 = 控制面 + DERP + STUN」，只要我们的服务端本来就有公网 443 和 TLS 证书。缺点：DERP 流量和业务流量共进程，限速/隔离要自己做；升级服务端会短暂中断所有中继中的会话（直连会话不受影响，DERP 只是信令）。

### G.3 要不要在我们的 Docker 里顺带跑一个 derper

**要。** 理由按重要性：

1. 公共 relay 可随时被撤、无 SLA、限速（G.1），拿来做产品依赖不可接受。
2. 固定 region 与地址稳定性完全在自己手里，绕开 issue #7。
3. 隐私：服务端与 agent 的 IP、连接时间不再经过第三方。
4. 成本几乎为零：同一台服务端机器、同一个域名（或子域名 `derp.`），多开 443/80/3478。

形态建议：**独立 derper 容器**（`docker compose` 里一个 service，和 Go 服务端并列），而不是嵌进服务端进程。原因：升级/重启解耦；可以独立限速；将来可以再加第二台做 mesh。构建时 pin 到与 tailcat go.mod 里相同的 `tailscale.com` 版本（`go install tailscale.com/cmd/derper@v1.103.0-pre.0.20260830144538-72780705eda8`），协议兼容最稳。

补充：STUN 只是让双方学到自己的公网 UDP 端点；打洞本身在两端之间进行。如果 derper 放在有公网 IP 的容器里，务必 `-p 3478:3478/udp`，否则永远只有 DERP 路径。

---

## F. 浏览器直连（web/ + wasm）：能力、限制、结论

### F.1 能做什么

- `web/main_js.go` 把 `tailcat.Server` 和 `tailcat.Client` 编译成 wasm，暴露 `tailcatListen(opts)` 和 `tailcatDial({addr, derpMapURL, privateKey, port})` 两个全局函数（web/main_js.go:28-35, 55-180），返回 `{read, write, closeWrite, close}` 的流对象（:212-263）。
- 浏览器通过 **WebSocket 连 DERP**（`derphttp` 在 `GOOS=js` 下自动切换，web/main_js.go:7-9）。所以浏览器确实可以**不经我们的 Go 服务端**、直接对被控机 agent 的 tailcat 地址拨号并拿到一条隧道内 TCP 流。
- 私钥可以持久化在 `localStorage`（web/app.js:5, 140-145），所以浏览器也能有稳定身份进白名单。
- 官方演示 https://tailscale.github.io/tailcat/ 能与 CLI 互传文件（README.md:38-42）。

### F.2 限制（都是硬的）

| 限制 | 证据 | 影响 |
|---|---|---|
| **只走 DERP，永远不直连** | `advertiseEndpoints` 在 js 下直接 return（tailcat.go:1215-1222）；index.html 明说要等 WebRTC（issue #4，web/index.html:24-30） | 全程吃 relay 带宽/限速；延迟 = 到 DERP 的两跳 |
| **无法选最近区域** | `PickBestRegion` js 版恒返回 0（pickregion_js.go:19-21）→ 随机选区（tailcat.go:1097-1108），只能靠 map 服务端按 IP 预过滤 | 用自建单区域 DERP 时无所谓 |
| **wasm 体积** | 未压缩约 27 MB（.github/workflows/webdemo-pages.yml:6-7）；已用 `WasmTags` 精简掉 6 MB（internal/buildtags/buildtags.go:25-32）；gzip 后 GitHub Pages 方案要浏览器 `DecompressionStream` 解压（web/app.js:95-104） | 手机首次加载慢；每次冷启动都要实例化 WireGuard + gVisor |
| **CPU/电量** | 用户态 WireGuard + TCP 栈在 wasm 里跑 | 手机上持续 SSH 会话不友好 |
| **DERP map 必须同源或 CORS** | web/app.js:11-36；`cmd/tailcat-web` 专门做了 `/derpmap.json` 代理（cmd/tailcat-web/tailcat-web.go:54-70） | 长地址可绕开 map 拉取 |
| **要自己在 JS/wasm 里实现 SSH 客户端或终端协议** | tailcat 只给字节流 | 这是最大的工程量 |
| **网页拿到的是 agent 地址 = 能力** | 地址泄露即可连（除非白名单） | 白名单里得放每个浏览器的 key，管理复杂 |
| 稳定性 | README 称 experimental（README.md:38） | |

### F.3 结论

**手机浏览器直连不值得作为主路径。** 全部经 Go 服务端中转：手机 ↔ 服务端走普通 HTTPS/WebSocket（终端协议、鉴权、审计都在服务端做），服务端 ↔ agent 走 tailcat（有直连、有 key 白名单、原生 Go）。浏览器直连留作以后的可选优化，触发条件是 tailcat 的 WebRTC 直连落地（issue #4）且我们有明确的「服务端不可达也要能连」需求。

如果一定要做浏览器直连的 PoC，成本最低的方式是复用 `web/main_js.go` 的两个函数 + 自建 DERP 长地址（省掉 map 拉取），在页面里跑一个 wasm 版 SSH 客户端或让 agent 侧的 ctrl 端口直接吐 PTY 字节流。

---

## I. 风险与坑

| # | 风险 | 证据 | 应对 |
|---|---|---|---|
| 1 | **Go 版本要求 1.27** | go.mod:3；flake.nix:16-18 注明比 nixpkgs 默认还新 | 我们的构建链要用 1.27+；本机已是 go1.27.0 |
| 2 | **无 API/CLI/wire 稳定性承诺** | tailcat.go:27-30；README.md:621-629 | vendor 或 pin 到 commit；wire 字段有测试锁定（wire_test.go），地址格式相对稳，但 `Client` 会拒绝旧版无 disco key 的地址（tailcat.go:1578-1580） |
| 3 | **tailscale.com 依赖是 pre-release 伪版本，且 tailcat 用了专为它加的上游钩子** | go.mod:22；`ForceDiscoKey`/`OnDERPRecv`/`SetPeerConfigFunc`/`SetPeerByIPPacketFunc` 在上游 wgengine/userspace.go:250-262, 677 | **tailcat 与 tailscale.com 必须成对升级**，不能单独 bump 任何一个；我们的 go.mod 里不要再直接依赖别的 tailscale.com 版本 |
| 4 | **reflect+unsafe 读 netstack 私有字段** | `tcpipStackOf` 会 `panic("... tailscale.com dep changed?")`（tailcat.go:667-677） | 只影响 `DrainTCP`；升级后跑一遍 DrainTCP 路径 |
| 5 | **CGO** | goreleaser `CGO_ENABLED=0`（.goreleaser.yaml:10）；我们实测 `CGO_ENABLED=0` 构建成功 | 纯 Go，交叉编译无痛 |
| 6 | **二进制体积** | 实测：release tags 18.2 MB，无 tags 21.5 MB（`-s -w`）；wasm 27 MB | 用 `build-tags.txt` 的 tag 列表构建我们的 agent（`internal/buildtags` 是 internal 包不能 import，但 tag 字符串可直接复制）；注意 `ts_omit_ssh` 不在列表里因为 release 保留了 ssh |
| 7 | **平台** | linux/windows amd64/arm64/armv7 官方发版（.goreleaser.yaml:18-29）；macOS 走 brew；内置 SSH 支持 linux/darwin/windows（tailcat_ssh.go:4, tailcat_ssh_windows.go） | 全覆盖 herdr 开发机；Windows 内置 SSH 走 ConPTY + PowerShell（tailcat_ssh_windows.go:24-46） |
| 8 | **公共 DERP 限速 / 可撤销** | README.md:35-36, 626-628 | 自建 DERP（G.3） |
| 9 | **重启后对象作废、`Ping` 不能探活、无 Remove 白名单** | D.3、C.6 | 自己写监督循环；撤销 = 重启或应用层拒绝 |
| 10 | **同一 client key 多进程并发使用会冲突** | D.4；`tcAddrForKey` 决定 IPv6（tailcat.go:1434） | 每个服务端副本独立 key |
| 11 | **服务端 `clients` 只增不减** | tailcat.go:1372-1387 | 客户端用固定 key；拓扑 B 下 agent 重装换 key 会残留旧 peer，重启服务端清理 |
| 12 | **全局副作用** | `netns.SetEnabled(false)`（tailcat.go:1484）、`tailcat.Verbose` 全局、`envknob`（`TS_DEBUG_*`）、`http.DefaultClient` 拉 map（tailcat.go:985） | 进程内不要混用其它 tailscale 组件；拉 map 走代理时注意 DefaultClient |
| 13 | **每个 Client/Server 一整套引擎** | tailcat.go:434-520, 1588-1661 | 拓扑 A 每 agent 一套；规模大走拓扑 B（H.3） |
| 14 | **威胁模型偏「自己连自己」** | SECURITY.md:14-26；已修过多轮漏洞（:32-62） | 开启白名单；不开 exit-node；不用 no-auth-ssh 对外；ctrl 协议自己做鉴权 |
| 15 | **`ServedTCPPorts` 过滤是静默丢包** | tailcat.go:384-385；tailcat_test.go:206-219 | 客户端拨错端口只会超时，排障时先看 OnTCP 是否被调用 |
| 16 | **进程退出丢 FIN** | tailcat.go:584-598 | 短命进程用 `DrainTCP`；长驻 agent 无关 |
| 17 | **`Server.AllowProxy` 是死字段** | tailcat.go:351-353，无读取处 | 不要依赖它 |
| 18 | **许可证** | BSD-3-Clause（LICENSE, .goreleaser.yaml:47） | 可商用、可闭源嵌入，保留版权声明即可 |
| 19 | **依赖面大** | go.mod 110 行，含 gvisor、wireguard-go、aws-sdk（间接）等 | 供应链审计范围大；`go mod why` 检查间接依赖是否真的链进二进制 |
| 20 | **短地址依赖 map 长期有效** | README.md:425-426 issue #7 | 只用长地址 + 自建 DERP |

---

## 决策建议汇总

| 问题 | 建议 | 依据章节 |
|---|---|---|
| 用 CLI 还是嵌库 | **嵌库，自写 agent 二进制**；CLI 只用于 PoC 与排障 | H.1 |
| 谁是 tailcat Server | MVP 按拓扑 A（agent=Server，服务端=Client）；把角色抽象在接口后，>100 台在线时切拓扑 B（服务端=单 Server，agent=Client + yamux） | H.2, H.3, D.4 |
| 地址形式 | 只用**长地址**（`Server.TailcatAddr()`），嵌自建 DERP 主机名，不依赖 tailcat.dev map | C.2, C.3, G |
| DERP | **自建 derper**，独立容器与 Go 服务端并列，pin 到 go.mod 里同一 tailscale.com 版本；开放 443/tcp、80/tcp、3478/udp | G.2, G.3 |
| 密钥 | agent 与服务端都用固定 `key.NodePrivate`，沿用 tailcat 的 `PrivateKey` JSON 格式落盘；服务端每个副本独立 key | C.5, D.4 |
| 白名单 | agent 安装时预置服务端公钥（无 allow-all 窗口）；做不到再用「配对窗口 + `AddAllowedClient`」 | C.6, H.4 |
| 二维码 | `{v, addr(长地址), token(一次性), host, os/arch/agent_ver, svc}`，byte mode | H.4 |
| 重连 | 库不做对象级重连；服务端每 agent 一个监督循环，`DiscoPing` 探活，失败即 `Close()` + 新建 Client；**不要用 `Ping` 探活** | D.3 |
| 多端口 | 直接用隧道内端口号：22→sshd、7000→herdr unix socket、7001→ctrl 协议；`ServedTCPPorts` 收紧 | E, B.1 |
| 浏览器直连 | **不做**为主路径；手机 ↔ 服务端 HTTPS/WS，服务端 ↔ agent tailcat | F.3 |
| 版本管理 | tailcat 与 tailscale.com 成对升级；Go ≥ 1.27；`CGO_ENABLED=0`；用 build-tags.txt 减 16% 体积 | I |

## 附：本次调研的实证数据

- `CGO_ENABLED=0 go build -tags "$(cat build-tags.txt)" -ldflags "-s -w" ./cmd/tailcat` → **18,170,016 字节**；不带 tags → 21,455,008 字节（本机 go1.27.0 linux/amd64，冷编译约 20s）。
- B 节两个示例已在 scratchpad 独立 module 中 `replace` 指向本地 tailcat 源码编译通过（`go build` + `go vet`），见 `scratchpad/verify/`。未做真实联网连接测试（需要一台公网 DERP）。
- B.1 示例 agent 自身体积：release tags + `-s -w` → **15,954,080 字节**（比完整 CLI 还小，因为没链 SFTP/ff 等）；不加 tags 不 strip → 29 MB。
- B.1/B.2 示例实际落盘的 key JSON 与 C.5 描述一致：`"Private": "privkey:…"`、`"ServerPublic": "nodekey:…"`、`"ServerDiscoPublic": "discokey:…"`；给了 `Region` 时会完整出现 `RegionID/RegionCode/RegionName/Nodes`（`RegionName` 为空串也会输出，因为上游 `tailcfg.DERPRegion` 该字段无 omitempty）。
