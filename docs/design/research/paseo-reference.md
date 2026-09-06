# Paseo 调研报告：对多用户 Web 版 herdr 客户端的启示

> 调研对象：`../paseo`（v0.7.2，npm workspaces monorepo）。只读调研，未修改任何文件。
> 目标：为「Go 服务端 + 现代前端 + Docker 部署 + 手机适配 + 设备配对二维码」的 herdr Web 客户端提炼可借鉴经验。
> 引用格式为 `文件路径:行号`，均相对于 paseo 仓库根目录。

## 0. 仓库结构速览

| 包 | 职责 | 对我们的对应物 |
| --- | --- | --- |
| `packages/server` | Node.js daemon：HTTP + WebSocket API、agent 生命周期、终端 PTY、relay 出站连接、内嵌 Web UI 静态服务 | Go 服务端 |
| `packages/protocol` | zod 定义的全部 wire schema（消息、二进制帧、连接 offer、endpoint 解析），server/app/cli 共同依赖，**不依赖 server** | Go 侧 schema 生成 TS 类型 |
| `packages/client` | daemon WebSocket 驱动 + `PaseoClient` SDK 门面（重连、request/response 关联、终端流路由、relay E2EE transport） | 前端 `@herdr/client` |
| `packages/relay` | E2EE 原语（tweetnacl）+ 加密通道状态机 + 一个 legacy 的 Cloudflare Durable Object relay 实现 | 若做 relay，可用 Go 重写 |
| `packages/app` | Expo（React Native + react-native-web）跨端客户端：iOS/Android/Web/Electron 共用 | 我们的前端 |
| `packages/cli` / `desktop` / `website` | Commander CLI / Electron 壳 / 官网 | 参考价值低 |
| `docker/` | 官方镜像 Dockerfile、entrypoint、compose 样例 | Docker 部署 |

---

## A. 设备配对流程

### Paseo 怎么做

**1. 二维码里编码了什么。** 二维码内容就是一条 URL：`https://app.paseo.sh/#offer=<base64url(JSON)>`。JSON 是 `ConnectionOfferV2`，schema 在 `packages/protocol/src/connection-offer.ts:9-17`，server 侧构造在 `packages/server/src/server/connection-offer.ts:30-50`：

```ts
// packages/protocol/src/connection-offer.ts:9-17
export const ConnectionOfferV2Schema = z.object({
  v: z.literal(2),
  serverId: z.string().min(1),          // 稳定 daemon 标识，也是 relay 的会话 id
  daemonPublicKeyB64: z.string().min(1),// daemon 的 Curve25519 公钥（base64）
  relay: z.object({
    endpoint: z.string().min(1),        // "relay.paseo.sh:443"
    useTls: z.boolean().optional(),
  }),
});
```

```ts
// packages/server/src/server/connection-offer.ts:43-50
export function encodeOfferToFragmentUrl({ offer, appBaseUrl }) {
  const json = JSON.stringify(args.offer);
  const encoded = Buffer.from(json, "utf8").toString("base64url");
  return `${args.appBaseUrl.replace(/\/$/, "")}/#offer=${encoded}`;
}
```

解码后的实际 JSON：

```json
{
  "v": 2,
  "serverId": "srv_AbCdEfGhIjKl",
  "daemonPublicKeyB64": "Q2FmZUJhYmUuLi4zMmJ5dGVzLi4u=",
  "relay": { "endpoint": "relay.paseo.sh:443", "useTls": true }
}
```

要点：
- 用 URL **fragment**（`#offer=`）而非 query，浏览器不把 fragment 发给服务器，配对信息不进 app.paseo.sh 的访问日志。
- 同一 URL 既是二维码内容也是「粘贴配对链接」的内容，手机端 `parseConnectionOfferFromUrl` 一处解析（`packages/protocol/src/connection-offer.ts:54-59`）。
- offer 里**没有密码、没有一次性 token**，仅 daemon 公钥 + relay 地址 + serverId。安全模型是「拿到二维码 = 拿到访问权」（`public-docs/security.md:53`「Treat it like a password」）。
- offer 是 **relay-only**（`pairing-offer.ts:25-32`：relay 未启用时返回 `url: null, qr: null`）。直连（LAN/Tailscale）不走二维码，用户手动输 host:port + 密码。

**2. serverId 与 daemon 密钥对的生成与持久化。**
- `serverId`：`srv_` + 9 字节随机数的 base64url（12 字符），写 `$PASEO_HOME/server-id`，权限 0600（`packages/server/src/server/server-id.ts:23-27, 76-82`）。可用 `PASEO_SERVER_ID` 覆盖。
- 密钥对：`nacl.box.keyPair()`，以 `{v:2, publicKeyB64, secretKeyB64}` 存 `$PASEO_HOME/daemon-keypair.json`，私有权限 + 原子写（`daemon-keypair.ts:16-20, 55-66`）。加载失败**静默重新生成**（`:50-52`），即所有已配对设备失效。

**3. 二维码渲染。** 服务端用 `qrcode` 包渲染 UTF-8 终端字符（`pairing-qr.ts:7-18`），CLI `paseo daemon pair` 直接打印；桌面/Web 用同一 URL 在前端渲染。

**4. 配对后凭证如何存储。** 手机端把 offer 解析后存为一个「host」记录，含 serverId、daemonPublicKeyB64、relay endpoint。后续每次连接：通过 relay 连 `wss://relay/ws?serverId=...&role=client&v=2`；用 offer 里的 daemon 公钥 + **本次新生成的临时客户端密钥对** 做 ECDH 握手（B 节）。客户端**不持久化自己的密钥对**（`packages/relay/src/encrypted-channel.ts:154-161`：`createClientChannel` 内每次 `generateKeyPair()`）。因此 daemon 侧不存在「设备身份」，只认「持有我公钥的人」。

**5. 配对 token 的生命周期。** 没有独立 token。offer 有效期 = daemon 密钥对寿命（永久）。

| 项 | Paseo 现状 |
| --- | --- |
| 一次性 | 否，同一二维码可无限次扫描 |
| 过期 | 否 |
| 与设备绑定 | 否，daemon 不记录已配对设备列表 |
| 撤销单个设备 | **不支持**。只能删 `daemon-keypair.json` 让 daemon 换密钥（全部设备失效），或关闭 relay |

**6. relay 启用消费流程。** 新装机 `daemon.relay.enabled: false`（`docs/architecture.md:158`），首次 `paseo daemon pair` / 桌面 Pair a device 时询问是否启用 relay，同意后 `DaemonConfigStore` 持久化并**热启动** relay transport，再生成 offer。`--relay` 跳过交互。

### 对我们的启示

1. **二维码载荷用「base64url(JSON) 放 URL fragment」**直接照抄：一份内容同时服务扫码、复制链接、深链；fragment 不进服务器日志；JSON 带 `v` 字段留演进空间。建议载荷：`{v, serverId, serverPubKey, endpoint:{wss}, pairingToken, expiresAt}`。
2. **补上 Paseo 缺的三件事**：一次性/短时效 pairing token（扫码后换长期 device token）、服务端「已配对设备列表」（deviceId、名称、平台、最近活跃、签发时间）、单设备撤销。多用户下是刚需，Paseo 单用户模型省掉了。
3. **设备侧凭证**：Paseo「客户端每次连接生成临时密钥、不持有身份」适合零知识 relay 但不适合多用户。我们应让设备在配对时生成长期密钥对或拿服务端签发的 device token，服务端按 userId → devices 存表。
4. **serverId + 私有文件权限 + 原子写**的持久化模式简单可靠，Go 侧 `os.WriteFile(tmp, 0600)` + `rename`。
5. **密钥丢失即全部设备失效**是可接受的降级，但要在 UI 提示「所有设备需重新配对」，Paseo 只打 warn 日志。

配对时序图：

```
 手机 App                      Relay                      Daemon(服务端)                 用户终端/桌面
    |                            |                             |<-- paseo daemon pair --------|
    |                            |                             |  loadOrCreateDaemonKeyPair   |
    |                            |                             |  getOrCreateServerId         |
    |                            |                             |  offer={v:2,serverId,        |
    |                            |                             |    daemonPublicKeyB64,relay} |
    |                            |                             |  url=app/#offer=b64url(json) |
    |                            |                             |--- 打印 QR(utf8) / 显示链接 ->|
    |<========== 扫码 / 粘贴链接（带外信道，含 daemon 公钥）===============================|
    | parseConnectionOfferFromUrl|                             |                              |
    | 保存 host{serverId,pubKey, |                             |                              |
    |   relay.endpoint}          |                             |                              |
    |-- WS ?serverId&role=client&v=2 -->|                      |                              |
    |                            |-- ctrl: {type:"connected",connectionId} -->|              |
    |                            |<-- WS ?serverId&role=server&connectionId --|              |
    |                            |    (daemon 为该连接开一条 data socket)      |              |
    |-- e2ee_hello{key:clientPub}(明文) -->| -- 原样转发 ------>|                              |
    |                            |                             | ECDH(daemonSecret, clientPub)|
    |<-- e2ee_ready(明文) -------|<-- 原样转发 -----------------|                              |
    | ECDH(clientSecret,daemonPub)|                            |                              |
    |== 之后全部 nacl.box 加密帧 ==|== relay 只见密文 ==========|                              |
    |-- hello{clientId,...}(加密) -->                          | 应用层 hello 握手（G 节）    |
```

---

## G. daemon 的 WebSocket API 设计（envelope / 请求-响应-事件 / 订阅 / 重连同步 / 心跳）

### Paseo 怎么做

**1. 两层 envelope。** 顶层是 WS 级消息，只有 4 种入站、2 种出站；业务消息统一包在 `{type:"session", message:{...}}` 里（`packages/protocol/src/messages.ts:7048-7105`）：

```ts
// packages/protocol/src/messages.ts:7057-7105（节选）
export const WSHelloMessageSchema = z.object({
  type: z.literal("hello"),
  clientId: z.string().min(1),
  clientType: z.enum(["mobile", "browser", "cli", "mcp", "hub"]),
  protocolVersion: z.number().int(),
  appVersion: z.string().optional(),
  capabilities: z.object({ voice, pushNotifications, selectiveAgentTimeline, ... }).passthrough().optional(),
});
export const WSSessionInboundSchema  = z.object({ type: z.literal("session"), message: SessionInboundMessageSchema });
export const WSSessionOutboundSchema = z.object({ type: z.literal("session"), message: SessionOutboundMessageSchema });
export const WSInboundMessageSchema  = z.discriminatedUnion("type", [WSPing, WSHello, WSRecordingState, WSSessionInbound]);
export const WSOutboundMessageSchema = z.discriminatedUnion("type", [WSPong, WSSessionOutbound]);
```

WS 层只管：连接握手（hello）、传输层保活（ping/pong）、把 session 消息交给 `Session.handleMessage`。所有 JSON 帧先 `JSON.parse` 再 `WSInboundMessageSchema.safeParse`，失败即记日志并（握手前）关连接（`websocket-server.ts:2176-2197, 2312-2330`）。

**2. hello 握手是强制的、有超时的。** 连接建立后服务端放进 `pendingConnections`，15 秒内没收到 `hello` 就以 4001 关闭（`websocket-server.ts:494-499, 1262-1283`）。hello 之前发任何 session 消息或二进制帧 → 4002 关闭（`:2107-2115, 2130-2157`）。`protocolVersion` 不匹配 → 4003（`:1495-1508`）。自定义关闭码：

| 码 | 含义 | 位置 |
| --- | --- | --- |
| 4001 | hello 超时 | `websocket-server.ts:495` |
| 4002 | 非法 hello / hello 前收到业务消息 | `:496` |
| 4003 | 协议版本不兼容 | `:497` |
| 4401 | daemon 鉴权失败 | `:114` |
| 1001 | 服务端关闭 | `:498` |

**3. 会话按 `(principalId, clientId)` 键可恢复。** hello 后服务端以 `sessionConnectionKey(principalId, clientId)` 查 `externalSessionsByKey`：命中则 `resumeSession`（把新 socket 挂到已有 Session，`existing.sockets.add(ws)`，清掉断线清理定时器），否则新建 Session（`websocket-server.ts:1541-1580, 1590-1620`）。这意味着：
- 一个 Session 可以同时挂多个 socket（多标签页共享一个 clientId）。
- 客户端断线重连时只要 clientId 不变，服务端侧的订阅状态（agent 订阅、终端流 slot、文件订阅等）**保留**，无需重新订阅。断线后 Session 保留一段时间再清理（`externalDisconnectCleanupTimeout`）。
- 每次 hello/resume 后服务端立刻回 `server_info`（`:1560, 1609`），里面带 `serverId, hostname, version, permissions, capabilities, features{...}`（`messages.ts:3382-3520`）。

**4. 三类消息如何区分。** 靠命名和 `requestId` 字段，而不是 envelope 里的 kind 字段：
- **请求**：`xxx_request` 或新式 `domain.ns.verb.request`，参数平铺在顶层，必带 `requestId`（`docs/rpc-namespacing.md:32-42`）。
- **响应**：`xxx_response` / `domain.ns.verb.response`，结果放在 `payload` 下，`payload.requestId` 回带（`rpc-namespacing.md:44-59`）。
- **错误**：统一 `{type:"rpc_error", payload:{requestId, error, requestType?, code?}}`（`messages.ts:3570`；client 侧 `daemon-client.ts:1683-1691` 用它拒绝 promise）。
- **事件/推送**：无 `requestId` 的出站消息，如 `agent_stream`、`project.update`、`terminal_stream_exit`。client 用 `messageHandlers: Map<type, Set<handler>>` 按 type 分发（`daemon-client.ts:1065-1068`）。

client 侧 `sendRequest` 的做法：先注册一个 waiter（谓词：`msg.payload.requestId === requestId` 或匹配 `rpc_error`），再发送，超时默认值 `DEFAULT_SESSION_RPC_TIMEOUT_MS`；连接断开时 `clearWaiters` 让所有挂起请求以「Connection lost」失败（`daemon-client.ts:1673-1715, 5984-5987`）。

**5. 二进制帧与 JSON 帧共用一条 WS。** 二进制帧首字节是 opcode，用于终端流（0x01-0x05）和文件传输（`packages/protocol/src/binary-frames/demux.ts:16-35`）。终端帧格式极简：`[opcode:1][slot:1][payload...]`（`binary-frames/terminal.ts:65-91`），`slot` 是服务端分配的 0-255 终端流槽位，避免每帧带 terminalId 字符串。服务端 `handleRawMessage` 先尝试 `decodeBinaryFrame`，失败再当 JSON（`websocket-server.ts:2181-2190`）。

**6. 订阅模型。** 没有通用 pub/sub 原语，而是按领域各有「订阅 RPC + 事件流」：
- agent 时间线：选择性订阅（`selectiveAgentTimeline`），客户端把「可见 agent + 最近 5 个」告诉服务端，只有这些 agent 的 `agent_stream` 会推过来（`docs/timeline-sync.md:127-143`）。
- 目录（projects/workspaces/agents）：`project.list` / `fetch_workspaces` / `fetch_agents` 既是拉取也隐式授予该 session 的增量推送。
- 终端：`subscribe`/`unsubscribe` 分配 slot，之后走二进制帧。
- 文件/diff/终端列表：各自的 subscribe RPC，client 侧在 `fileSubscriptions`、`checkoutDiffSubscriptions`、`terminalDirectorySubscriptions` 三张 Map 里记住，用于重连后重放（`daemon-client.ts:1085-1097`）。

**7. 重连后的状态同步：快照 + 单调序列 + generation。** 这是 Paseo 最值得抄的部分。

目录同步（`packages/server/src/server/directory-sync/index.ts`）：
- 服务端为 projects / workspaces / agents 各维护一个 `VersionedCollection`，每个实体只保留**最新投影**（不是事件日志），每次变更递增该集合的 `seq`；整个服务端有一个启动时随机生成的 `generation`（`:39-54`）。
- 客户端拉取时带 cursor `{generation, afterSeq}`；服务端返回 `seq > afterSeq` 的实体（含 tombstone），响应带 `sync:{generation, headSeq}`（`:58-66, 133-137`）。
- cursor 缺失、过期或 generation 不同 → 返回全量快照（`docs/architecture.md:117-120`）。
- 实时事件（`project.update` 等）也带 `syncSeq`，客户端据此去重、判断是否落后（`:98-112`）。

agent 时间线同步（`docs/timeline-sync.md`）：
- 「live stream 负责即时性，`fetch_agent_timeline_request` 负责正确性」。
- 每条时间线项带 `epoch`（时间线被重写时变化）和 `seqStart/seqEnd`。客户端检测到 seq 缺口就拉 `direction:"after"` 的分页，直到 `hasNewer:false`。
- 重连后拉「最新一页 tail」，和本地范围比对：同 epoch 同 maxSeq → no-op；重叠/相邻 → 只应用更新的；中间有缺口或 epoch 变了 → 原子替换（`timeline-sync.md:72-89`）。
- 分页大小是「投影项」数不是原始行数，因为 relay 有帧大小上限（`:33-37`）。
- 重试退避：1s 起翻倍到 30s 上限，成功/重连/可见性变化时重置（`:48-52`）。

**8. 心跳有两层。**
- WS 传输层：`{type:"ping"}` / `{type:"pong"}`，无 requestId（`messages.ts:7049-7055`）；client 每 10s 发一次，15s 超时，连续 2 次失败才触发重连（`daemon-client.ts:922-924, 6066-6075`）。同时 client 还有一个带 `requestId` 的 session 级 `ping` 用于测 RTT（`messages.ts:2773-2776`），二者可 dedupe 但「测延迟的 ping 超时不触发断线判定」（`daemon-client.ts:1058-1061`）。
- 应用层 presence：`client_heartbeat{deviceType, focusedAgentId, focusedTerminalId, lastActivityAt, appVisible}`（`messages.ts:2761-2771`），**只用于通知路由**（决定要不要推送、要不要清除 attention），明确「不作为投递正确性的门」（`timeline-sync.md:22-31`）。
- 服务端 relay 控制通道用 WebSocket 协议级 ping（不是 JSON），10s 一次，30s 无 pong 则 terminate 重连（`relay-transport.ts:56-58, 214-248`）。

**9. 客户端重连策略。** `delay = min(1500ms * 2^attempt, 30000ms)`（`daemon-client.ts:916-917, 6020-6025`）；每次重连 dispose 旧 transport、清 waiters、清终端 slot、清 `lastServerInfoMessage`；open 后立即发 hello。浏览器 `onerror` 常无细节，故延迟 250ms 等 close 事件拿真正原因（`:1288-1313`）。

**10. 协议兼容纪律。** `docs/protocol-compatibility.md`：新字段必 optional；不许 optional→required、不许删字段、不许收窄类型；wire schema 禁用 `.transform/.catch/.preprocess`；新功能通过 `server_info.features.*` 一次性 gate，不做降级路径；每个兼容 shim 必须带 `COMPAT(name): added in vX, remove after <date>` 注释，`rg "COMPAT\("` 就是清理清单。

### 对我们的启示

1. **照抄两层 envelope + 强制 hello + 自定义 4xxx 关闭码。** Go 侧：`/ws` 升级后进入 pending 状态，15s 内必须收到 `hello{clientId, clientType, protocolVersion, deviceToken/authToken, capabilities}`；回 `server_info{serverId, version, features, userId, permissions}`。业务消息统一 `{type:"session", message}` 或直接平铺 type，二者取其一即可，关键是 WS 层与业务层分离。
2. **请求/响应/事件用命名 + requestId 区分**足够，不必再引入 `kind` 字段。建议一开始就用 Paseo 的新式点分命名 `domain.verb.request/.response`，错误统一 `rpc_error{requestId, code, error}`。Go 用一个 `map[string]Handler` 按 type 分发，`requestId` 用客户端生成的 UUID 即可。
3. **可恢复会话按 `(userId, clientId)` 键保留一段时间**，重连后订阅不丢。多用户下 principalId 天然就是 userId，Paseo 里已经为此预留了 `SessionAdmission{principalId}`（`websocket-server.ts:123-125, 501-503`，当前恒为 `"owner"`）。
4. **目录同步用「generation + 每实体最新投影 + 单调 seq + tombstone」**而不是事件日志：实现简单、内存有界、重连后一次拉取即可对齐。herdr 的 workspace/tab/pane/agent 树正好适用：每个实体一行、一个全局 seq、客户端带 `{generation, afterSeq}` 拉增量。
5. **终端流用 `[opcode][slot][bytes]` 二进制帧**，与 JSON 帧同一条 WS，首字节判别。slot 由服务端在 subscribe 响应里分配。Go 里 `gorilla/websocket` 或 `nhooyr.io/websocket` 都能直接区分 text/binary 帧。
6. **心跳分层**：传输层 ping/pong 10s/15s/2 次，只做断线判定；presence 心跳只影响通知与 attention，不影响数据正确性。
7. **兼容纪律**直接搬进我们的 CONTRIBUTING：`features` 位 gate、COMPAT 标签带日期、schema 只加 optional。
8. **不适用的部分**：Paseo 的 `Session` 是单进程内存对象（agent 订阅、终端 slot 都在内存里），多实例部署时无法水平扩展。我们若要多副本，需要把会话状态和 seq 放到 Redis/DB，或用 sticky session。

---

## B. Relay 与端到端加密

### Paseo 怎么做

**1. 拓扑：daemon 出站、客户端入站、relay 只做字节转发。** daemon 不开任何入站端口，主动连到 relay；手机连到同一个 relay；relay 按 `serverId` 把两边配在一起并原样转发帧（`packages/relay/src/types.ts:4-9`）。生产 relay 是独立仓库的 Elixir 服务 getpaseo/paseo-relay；本 monorepo 里 `cloudflare-adapter.ts` 是遗留的 Cloudflare Durable Object 实现（`docs/architecture.md:168`），但协议是同一套，可作为规范阅读。

**2. relay 协议（v2）。** 纯 WebSocket，URL query 承载路由信息，帧体不做任何封装（`packages/protocol/src/daemon-endpoints.ts:191-213`，`cloudflare-adapter.ts:417-443`）：

```
wss://relay.paseo.sh:443/ws?serverId=<srv_xxx>&role=server&v=2                 # daemon 控制通道（每 serverId 一条）
wss://relay.paseo.sh:443/ws?serverId=<srv_xxx>&role=server&v=2&connectionId=<c> # daemon 每客户端一条数据通道
wss://relay.paseo.sh:443/ws?serverId=<srv_xxx>&role=client&v=2                 # 客户端（relay 分配 connectionId）
```

- v1 是「一个 server socket + 一个 client socket」直连对；v2 引入**控制通道 + 每客户端数据通道**，让 daemon 能给每个手机单独建一条 E2EE 通道（`types.ts:17-27`，`cloudflare-adapter.ts:120-128`）。
- 控制通道上 relay → daemon 的 JSON 消息只有三种：`{type:"sync", connectionIds:[...]}`（daemon 刚连上时给全量在线客户端列表）、`{type:"connected", connectionId}`、`{type:"disconnected", connectionId}`（`cloudflare-adapter.ts:394-408, 545`；daemon 侧解析 `relay-transport.ts:49-54, 309-329`）。daemon 收到 connected/sync 就为每个 connectionId 开一条 `role=server&connectionId=` 的数据 socket（`relay-transport.ts:345-393`）。
- 客户端先到、daemon 数据通道还没建好时，relay 为该 connectionId **缓存最多 200 帧**，daemon 接上后 flush（`cloudflare-adapter.ts:252-275, 410-412`）。
- 同 connectionId 允许多个 client socket（多标签页），最后一个断开才通知 daemon 关数据通道（`:528-546`）；daemon 数据通道断开则以 1012 踢客户端让其重连重握手（`:549-558`）。
- 控制通道保活用 **WebSocket 协议级 ping**（Cloudflare 边缘自动回 pong，不唤醒 DO），10s 一次、30s 无响应 terminate 重连（`relay-transport.ts:56-58, 214-248`）。重连退避 `min(30s, 1s*attempt)`（`:333-343`）。
- relay 侧健康检查 `GET /health` → `{"status":"ok"}`（`cloudflare-adapter.ts:584-588`）。

**3. E2E 加密算法。** tweetnacl，纯 JS，浏览器/RN/Node 三端一致（`packages/relay/src/crypto.ts:1-13`）：

| 项 | 实现 |
| --- | --- |
| 密钥交换 | Curve25519 ECDH：`nacl.box.before(peerPub, ourSecret)` 得 32 字节共享密钥（`crypto.ts:134-150`）；先用 `nacl.scalarMult` 检查全零结果拒绝低阶点 |
| 对称加密 | XSalsa20-Poly1305（`nacl.box.after` / `nacl.box.open.after`） |
| 帧格式 | `[nonce 24B][ciphertext]`，nonce 每帧随机（`crypto.ts:156-165`） |
| 传输表示 | 协商 `binaryCiphertext` 能力后：应用层文本 → base64 密文放 WS text 帧；应用层二进制 → 原始密文放 WS binary 帧，opcode 端到端保留（`encrypted-channel.ts:462-483`，`SECURITY.md:23-25`） |
| 密钥寿命 | daemon 密钥对持久；客户端密钥对**每次连接临时生成**，故每个 session 的共享密钥都不同（`encrypted-channel.ts:159`） |
| 重放防护 | 跨 session 有（密钥不同）；**同 session 内没有**（无计数器、不追踪 nonce，`SECURITY.md:37`） |

**4. 握手消息（明文 JSON，relay 可见但只含公钥）：**

```ts
// packages/relay/src/encrypted-channel.ts:56-69
interface E2EEHelloMessage { type: "e2ee_hello"; key: string /*client pub b64*/; capabilities?: { binaryCiphertext?: boolean } }
interface E2EEReadyMessage { type: "e2ee_ready"; capabilities?: { binaryCiphertext?: boolean } }
```

握手细节：
- 客户端每 1s 重发 `e2ee_hello` 直到收到 `e2ee_ready`（`:118, 203-210`），因为 daemon 数据通道可能还没接上。
- daemon 收到 hello 后先 `Object.assign(transport, {onmessage: bufferNext})` 缓冲后续帧，避免异步派生密钥期间把第一条密文当成第二个 hello（`:265-272`）。
- 通道打开后再收到 `e2ee_hello`：若用同一客户端公钥推出的共享密钥相同就重发 `e2ee_ready`（客户端漏收）；密钥不同则以 1008 关闭，**拒绝密钥轮换**（`:395-399, 503-525`）。
- 握手前的 send 进队列（最多 200 条，超出丢最旧），open 后 flush（`:462-469, 494-501`）。
- 加密通道上收到明文 JSON 业务消息 → 视为协议错位，1011 关闭（`:402-409`）。解密失败也 1011 关闭而不是抛错，让上层走正常重连（`:448-459`）。

**5. relay 看得见什么。** IP、时间、帧大小、serverId/connectionId、明文的 `e2ee_hello/e2ee_ready`（只有公钥）。看不见：任何业务消息、终端流、文件内容；无法伪造（Poly1305 认证）；无法冒充 daemon（没有 daemon 私钥推不出共享密钥）（`SECURITY.md:27-37`）。

**6. 自建 relay。** 配置 `daemon.relay.endpoint` / `publicEndpoint` / `useTls` / `publicUseTls`（或 `PASEO_RELAY_*` 环境变量）（`docs/architecture.md:162-163`，`pairing-offer.ts:34-37`）。区分 daemon 侧 endpoint 与写进二维码的 public endpoint，方便 daemon 走内网、手机走公网。relay 本身只需实现：`/ws` 按 `serverId+role+v+connectionId` 配对转发、`/health`、v2 的三种控制消息、离线缓存。Go 用 `gorilla/websocket` 几百行可实现。

**7. 性能注记。** 加密在 daemon 主事件循环上跑纯 JS tweetnacl，是已知瓶颈（`docs/terminal-performance.md:47`）；大 `agent_stream` 帧会拖慢终端回显。协商 binary 密文避免 base64 膨胀。

### 对我们的启示

1. **是否需要 relay 取决于部署形态。** herdr Web 版是「Go 服务端 + 反向代理 + HTTPS」的常规 SaaS/自托管形态，手机浏览器直接 `wss://` 连服务端即可，**relay 不是必需**。relay 的价值在于 daemon 跑在用户笔记本/NAT 后面的场景。若 herdr 后续要支持「用户本机 herdr daemon 被云端 Web 前端访问」，再引入 relay。
2. **若做 relay，直接抄 v2 协议**：控制通道 + 每客户端数据通道 + `sync/connected/disconnected` 三条控制消息 + 200 帧离线缓存 + 协议级 ping。这套设计经过生产验证，Go 实现体量小。
3. **E2EE 选型可换成 Go 生态更自然的 libsodium/`golang.org/x/crypto/nacl/box`**，wire 格式与 tweetnacl 完全兼容（同为 NaCl box），前端仍用 tweetnacl 或 libsodium.js。若不做 relay，TLS 到反代 + 服务端鉴权已够，不需要应用层 E2EE。
4. **值得单独借鉴的工程细节**：握手期间缓冲帧；hello 幂等重发；拒绝密钥轮换；解密失败走 close 而非 error 以触发干净重连；send 队列有界。
5. **Paseo 明确承认的缺口**：session 内无重放防护。我们若做 E2EE，加一个单调递增的 64 位计数器进 nonce 前 8 字节即可补上。

relay + E2EE 时序图（v2）：

```
 手机 App                          Relay(无状态转发)                          Daemon
    |                                    |                                       |
    |                                    |<-- WS ?serverId&role=server&v=2 ------|  控制通道，daemon 启动即连
    |                                    |--- {type:"sync",connectionIds:[]} --->|
    |                                    |    (协议级 ping/pong 10s 保活)          |
    |-- WS ?serverId&role=client&v=2 --->|                                       |
    |   (relay 分配 connectionId=c1)     |--- {type:"connected",connectionId:c1}->|
    |-- e2ee_hello{key:cPub,caps}(text)->| 缓存(≤200帧)                           |
    |                                    |<-- WS ?serverId&role=server&connectionId=c1  数据通道
    |                                    |--- flush 缓存帧 ---------------------->|
    |   (每 1s 重发 hello 直到 ready)     |                                       | shared=box.before(cPub,dSecret)
    |<-- e2ee_ready{caps}(text) ---------|<--------------------------------------|
    | shared=box.before(dPub,cSecret)    |                                       |
    |== [nonce24][box(hello{clientId})] =>|== 原样转发（text:b64 / binary:raw）==>| 解密 → 应用层 hello → server_info
    |<= [nonce24][box(server_info)] =====|<======================================|
    |== 业务 JSON / 终端二进制帧 ========>|=====================================>|
    |   ...                              |                                       |
    | (最后一个 client socket 断开)       |--- {type:"disconnected",c1} --------->| 关闭数据通道 c1
    | (daemon 数据通道断开)               |--- close 1012 "Server disconnected" ->| 手机重连并重新握手
```

---

## D. 认证

### Paseo 怎么做

**1. 单一共享密码，bcrypt 存储。** `PASEO_PASSWORD` 环境变量（明文，启动时 hash）或 `paseo daemon set-password` 写入 `config.json` 的 `auth.password`（`$2b$12$...`），cost 12（`packages/server/src/server/auth.ts:5, 48-50`；`public-docs/configuration.md:159-189`）。没有用户名、没有多用户、没有 token 签发。

**2. HTTP 鉴权：`Authorization: Bearer <password>`**，Express 中间件对每个请求 `bcrypt.compare`（`auth.ts:90-120`）。豁免：`OPTIONS` 预检、`/api/health`、以及两个「自带凭证」的路由 `/api/files/download` 与 `/mcp/agents`（`auth.ts:122-133`）。`/mcp/agents` 接受 daemon 每次启动随机生成的 capability token（只注入给自己拉起的 agent），用 `timingSafeEqual` 比较（`:143-162`）。

**3. WebSocket 鉴权：走 `Sec-WebSocket-Protocol` 子协议。** 客户端在 WS 握手时声明子协议 `paseo.bearer.<password>`（`daemon-client.ts:1208`）；服务端在 `handleProtocols` 里挑出这个子协议（`websocket-server.ts:808, 2867-2882`），连接建立后 `attachAuthenticatedSocket` 解析并 bcrypt 校验，失败以 **4401** 关闭（`:114, 2905-2925`）。原因：浏览器 `WebSocket` 构造函数不能设自定义 header（`SECURITY.md:49`）。非浏览器客户端同时也带 `Authorization` header（`daemon-client.ts:1200-1207`），但服务端 WS 路径只看子协议。

```ts
// packages/server/src/server/auth.ts:63-88
export function extractWsBearerProtocol(value) {          // "paseo.bearer.<token>, other"
  for (const protocol of value.split(",")) {
    const segments = protocol.trim().split(".");
    if (segments[0] === "paseo" && segments[1] === "bearer" && segments.length >= 3) return trimmed;
  }
}
export function extractWsBearerToken(protocol) { return segments.slice(2).join("."); }  // token 里可含 "."
```

**4. 升级阶段的防御层。** `verifyClient` 先检查 `Host` 头是否在 allowlist（默认 localhost、*.localhost、任意 IP；`daemon.hostnames` 追加；防 DNS rebinding），再检查 `Origin`（同源或在 `cors.allowedOrigins` 里，无 Origin 的非浏览器客户端放行）（`websocket-server.ts:verifyWsUpgrade`；`hostnames.ts:57-74`）。

**5. 密码在客户端如何存。** 直连 host 记录里明文存 `password` 字段（`packages/protocol/src/host-connection-schema.ts:3-9`），CLI 也支持 `tcp://host:6767?password=...` URI（`daemon-endpoints.ts:85-121`）。Web UI 静态文件不鉴权，登录页能渲染，API/WS 要密码（`public-docs/web-ui.md:99`）。

**6. 多用户支持：没有，但留了钩子。** `SessionAdmission{principalId}` 恒为 `"owner"`（`websocket-server.ts:123-125, 501-503`），会话键 `sessionConnectionKey(principalId, clientId)`。`server_info.permissions` 是 `DaemonPermission[]`（`daemon.read/manage, workspace.read/write/manage, access.manage ...`，`messages.ts:98-110`），`authorization/operation-permissions.ts` 定义了操作→权限映射，但 owner 拥有全部。Hub（外部服务连 daemon）用独立的 enrollment token 换长期凭证（`public-docs/security.md:134-142`），是 daemon 里唯一「非 owner principal」的雏形。

### 对我们的启示

1. **WS 鉴权走子协议这一招要抄。** 浏览器 WS 不能带 header 是硬约束；备选是 query string（会进访问日志）或首帧鉴权（要多一个状态）。Paseo 的 `Sec-WebSocket-Protocol: herdr.bearer.<token>` 在升级阶段就能拒绝，且 token 不进 URL 日志。Go 侧 `websocket.Upgrader.Subprotocols` + 在 `CheckOrigin`/升级前读 `r.Header["Sec-Websocket-Protocol"]` 即可。注意：服务端必须在 101 响应里回显选中的子协议，否则浏览器会断开。
2. **把 Paseo 的「单密码」换成正规多用户模型**：用户表 + 密码 bcrypt/argon2id + 登录签发 JWT 或 opaque session token（短期 access + 长期 refresh）；设备配对签发 **device token**（绑定 userId + deviceId，可单独撤销）。WS hello 携带的 token 决定 `principalId = userId`，会话键 `(userId, clientId)`，权限按用户/角色算。Paseo 的 `DaemonPermission` 枚举和 `operation-permissions.ts` 可作为权限粒度的起点。
3. **`/api/health` 无鉴权 + 其他全部鉴权 + OPTIONS 放行**是正确的中间件默认，Docker HEALTHCHECK 依赖它。
4. **Host allowlist 与 Origin 校验**在 Go 里同样便宜，作为反代之后的纵深防御保留；默认放行 IP 与 localhost 的策略对自托管友好。
5. **不要在客户端明文存密码**（Paseo 的直连 host 是这么做的）。我们存的是 device token/refresh token，泄露面更小且可撤销。

---

## E. Web / 移动客户端的终端、断网重连与推送

### Paseo 怎么做

**1. 终端组件：xterm.js 为核心，三种宿主。** 依赖 `@xterm/xterm` 6.x + addon-fit/webgl/search/web-links/unicode11/image/clipboard/ligatures（`packages/app/package.json:68-76`）。统一契约 `TerminalEmulatorHandle{writeOutput, restoreOutput, renderSnapshot, paste, copySelection, clear, claimSize, showKeyboard, blur}` 与 `TerminalEmulatorProps`（`packages/app/src/components/terminal-emulator-contract.ts:14-71`），三个实现：

| 实现 | 文件 | 场景 |
| --- | --- | --- |
| `terminal-emulator.tsx`（`"use dom"`） | Web / Electron，xterm 直接挂 DOM | 桌面与浏览器 |
| `terminal-emulator-webview.native.tsx` | iOS/Android 旧渲染器：`react-native-webview` 里跑预编译的 xterm HTML，RN ↔ WebView 用 `postMessage`/`injectJavaScript` 桥接 `mount/writeOutput/renderSnapshot/paste/resize/setTheme/...`（`:34-60, 298-356, 504-527`） | legacy |
| `terminal-emulator-native-grid.native.tsx` | iOS/Android 新渲染器：`@xterm/headless` 在 JS 线程解析 VT，自绘网格（`terminal-grid-view.native.tsx`），原生 `TextInput` 收输入 | 默认（`terminal-renderer-capability.ts:3-11`） |

移动端终端交互要点：
- **虚拟键盘**：一个 1×1 的隐藏 `TextInput` 承接系统键盘（`terminal-input.native.tsx:15-16`）；用 key-press 走 ASCII 路径、text-change diff 走 IME 路径，专门处理 CJK 组合输入与自动纠错（`:46-58`）。
- **修饰键工具栏**：Ctrl/Shift/Alt 三个可锁定的按钮 + 键盘显隐切换按钮（`terminal-pane.tsx:96-100, 125-141, 169-181`），修饰键状态是 `pendingModifiers`，消费后回调 `onPendingModifiersConsumed`（contract `:57, 68`）。
- **键盘避让**：`react-native-keyboard-controller` 拿键盘高度 → `keyboardInset` 传给终端；键盘变化后按 `[0,48,144,320]ms` 多次 refit（`terminal-pane.tsx:92, 226-231, 405-434`）。
- **手势**：移动端左右滑动切换面板（`swipeGesturesEnabled = isMobile`，`:225`）；终端有选区时屏蔽「打开侧栏」手势避免冲突（`useBlockMobilePanelOpenGestures`，`:268`；`docs/mobile-panels.md:77-80`）。
- **尺寸归属**：`resize` 帧带 `intent: "claim" | "update"`，只有聚焦/交互的连接能 claim PTY 尺寸，闲置的手机不会把桌面终端缩成 40 列（`binary-frames/terminal.ts:4-8`，`docs/terminal-performance.md:30`）。

**2. 终端数据管线（`docs/terminal-performance.md:7-16`）：** pty（node-pty，独立 worker 进程）→ headless xterm 解析（为快照保真）→ 5ms 合并器 → IPC → daemon 主进程 → 每客户端流合并器 → 2 字节头二进制帧 → client 解码 → stream router → xterm.write。关键不变量：合并器是 leading+trailing 节流（首字节立即刷）；快照回退受背压门控（输出 > 256KB **且** `bufferedAmount` > 4MB 才发全量快照）；隐藏但保留的终端 tab 继续消费流，切 tab 不重订阅。

**3. 消息流 UI 与终端 UI 的关系。** 二者是并列的 pane 类型，共享 workspace 布局（`workspace-layout-store`），都遵循「retained panel」模型：隐藏 pane 保持挂载并继续接收数据，靠 LRU 限制活跃流数量（`mobile-panels.md:100-104`）。终端活动（working/idle/needs-input）通过 agent hook 上报到 daemon 的 `/api/terminal-activity`，再以与 agent 相同的「运行中圆点 / 已完成绿点 / 需要输入」状态渲染到 tab 上（`docs/terminal-activity.md:7-9, 75`）。

**4. 断网 / 后台重连。**
- client 层：见 G 节，`min(1.5s*2^n, 30s)` 指数退避、10s liveness ping、连续 2 次超时才断线。
- 服务端为断线会话保留 **90 秒**（`EXTERNAL_SESSION_DISCONNECT_GRACE_MS`，`websocket-server.ts:493, 1890-1911`），期间同 clientId 重连即恢复全部订阅；超时才 `cleanupConnection`。
- App 层：`HostRuntimeController` 维护每 host 的 `idle/connecting/online/offline/error` 状态机（`host-runtime.ts:78`），`online` 转换时重建目录订阅并做 cursor 增量对齐（`docs/architecture.md:100-115`）。
- 可见性：`AppState.addEventListener("change")` + Web 的 `visibilitychange/focus/blur` 统一成 `getIsAppActivelyVisible()`（`hooks/use-agent-attention-clear.ts:71-84`），可见性变化会重置时间线同步退避（`timeline-sync.md:50-52`）并通过 `client_heartbeat.appVisible` 上报服务端。
- 离线缓存：IndexedDB（Web/Electron）/ expo-sqlite（原生）的 replica row store，键 `(serverId, kind, id)`，32MiB 上限按 host LRU 淘汰；离线打开先画缓存再在线对齐（`docs/data-model.md:534-552`，`architecture.md:100-108`）。

**5. 推送通知（仅原生 App）。**
- 客户端：`expo-notifications` 取 Expo push token（需 EAS projectId），按 serverId 存 AsyncStorage，连接成功即 `registerPushToken`（`push-notifications/internal/subscriptions.ts:31-85`）；删除 host 时若 daemon 支持 `features.pushTokenRevocation` 则 `unregisterPushToken`（`:87-102`）。
- 服务端：token 有 48h 租约，每次连接续期，存私有文件（`push/index.ts:8, 25`；`push/token-store.ts:34-45`）；发送走 Expo Push API `https://exp.host/--/api/v2/push/send`，每批 ≤100，`DeviceNotRegistered/InvalidCredentials` 自动吊销 token（`push/push-service.ts:24-25, 99-105`）。
- 决策：agent 需要注意（finished/error/permission）时，遍历订阅该 agent 的会话取 presence（`client_heartbeat` 上报），`computeNotificationPlan`：有可见且聚焦该 agent 的客户端 → 不通知；有 3 分钟内活跃的客户端 → 只给最近活跃那一个发应用内通知；都没有 → 推送（`websocket-server.ts:2507-2555`；`agent-attention-policy.ts:3, 44-60`）。
- Web 端没有推送（`push-notifications/index.web.ts` 为空实现），只有应用内通知。

### 对我们的启示

1. **Web 终端直接用 xterm.js + fit/webgl/web-links/search/unicode11**，这套组合成熟。我们没有 RN，不需要 WebView 桥或自绘网格；但**移动浏览器**上的痛点与 Paseo 原生端相同，需要自己解决：隐藏 `<textarea>`/`contenteditable` 承接软键盘并处理 IME composition（xterm 自带 helper textarea，但 iOS Safari 的 composition 事件需额外处理）；Ctrl/Alt/Esc/Tab/方向键的**修饰键工具栏**（移动键盘没有这些键）；用 `visualViewport` API 做键盘避让并多次 `fit()`。
2. **终端帧协议照抄**：`[opcode][slot][bytes]`，opcode 含 output/input/resize/snapshot/restore；resize 带 `claim/update` 意图解决多设备争抢 PTY 尺寸，这对「手机 + 桌面同时看同一 pane」是必需的。服务端 5ms 合并 + 背压门控快照的策略在 Go 里同样适用（`bufferedAmount` 对应 `conn` 写队列长度）。
3. **重连策略三件套**：客户端指数退避 + liveness ping；服务端 90s 会话宽限期按 `(userId, clientId)` 恢复订阅；重连后目录/时间线用 cursor 增量对齐。加上 `visibilitychange` 时立即触发一次重连尝试与同步。
4. **推送**：Web 版可用 Web Push（VAPID + Service Worker），思路与 Paseo 相同：订阅按 `(userId, deviceId)` 存服务端、带租约、投递失败 410 即吊销；通知决策沿用「有人正看着就不推、有人在线就应用内、都没人才推送」的 presence 策略，presence 来自心跳。
5. **离线缓存**可后置。Paseo 的 IndexedDB replica 是为「打开 App 先看到东西」服务的，Web 版首屏靠网络即可；若要做，`(serverId, kind, id)` 键 + 单调 seq checkpoint 的模型可直接搬。

---

## F. Docker 部署

### Paseo 怎么做

**1. 单镜像、单进程、单端口。** `docker/base/Dockerfile`：
- 两阶段构建：`source-pack` 阶段 `npm ci` 后把 7 个 workspace `npm pack` 成 tgz（`:20-28`）；运行阶段 `node:22-bookworm-slim` 全局安装这些 tgz（`:60-68`），这样镜像里装的就是发布产物，不带源码和 dev 依赖。
- 运行时依赖只有 `bash ca-certificates curl git gosu lbzip2 openssh-client procps tini`（`:46-58`）。
- 创建 uid/gid 1000 的 `paseo` 用户，预建 `/workspace`、`$PASEO_HOME`、`~/.claude`、`~/.codex`、XDG 目录并 chown（`:70-92`）。
- `ENTRYPOINT ["/usr/bin/tini","--","/usr/local/bin/paseo-docker-entrypoint"]`（`:105`）；entrypoint 以 root 启动，只做「确保挂载目录存在且属主正确」，然后 `gosu paseo node <server-entry>` 降权（`docker/base/rootfs/usr/local/bin/paseo-docker-entrypoint:32-58, 75-78`）。传了参数就 `gosu paseo "$@"`，方便 `docker exec` 跑 CLI。
- 没设 `PASEO_PASSWORD` 时 entrypoint 打 WARNING 但**不拒绝启动**（`:60-66`）。

**2. 环境变量默认值（`Dockerfile:32-44`）：**

```
HOME=/home/paseo  PASEO_HOME=/home/paseo/.paseo  PASEO_LISTEN=0.0.0.0:6767
PASEO_WEB_UI_ENABLED=true  PASEO_LOG_FORMAT=json  PASEO_LOG_LEVEL=info
CLAUDE_CONFIG_DIR=/home/paseo/.claude  CODEX_HOME=/home/paseo/.codex  XDG_*=/home/paseo/...
```

用户侧常用：`PASEO_PASSWORD`、`PASEO_HOSTNAMES`（反代域名 allowlist）、`PASEO_TRUSTED_PROXIES`、`PASEO_RELAY_ENABLED`、provider 的 `OPENAI_API_KEY/ANTHROPIC_API_KEY/*_BASE_URL` 透传给 agent。

**3. 卷。** 两个：`/home/paseo`（daemon 状态 + agent 凭证，声明为 `VOLUME`）与 `/workspace`（代码）（`public-docs/docker.md:98-107`）。文档明确提醒 Linux 上 bind mount 要对 uid 1000 可写，或用 `--user`。

**4. 健康检查。** `HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3`，用 `node -e` 请求 `127.0.0.1:<port>/api/health`，从 `PASEO_LISTEN` 解析端口（`Dockerfile:102-103`），不依赖 curl。`/api/health` 是唯一免鉴权接口。

**5. agent CLI 不打进基础镜像。** 用户自建子镜像 `FROM ghcr.io/getpaseo/paseo:latest; USER root; RUN npm i -g @openai/codex @anthropic-ai/claude-code`（`docker/Dockerfile.agents.example`），保留 root 让 entrypoint 降权（`public-docs/docker.md:68-87`）。凭证通过 `docker exec -it --user paseo paseo claude` 登录一次持久化到卷。

**6. 反向代理建议（`public-docs/web-ui.md:101-189`）。** 必须：转发 WS upgrade、`proxy_buffering off`、长 read/send timeout（3600s）、`client_max_body_size 100m`、保留 `Host` 与 `X-Forwarded-Proto`。服务端用 `X-Forwarded-Proto` 决定注入给前端的连接提示是 `ws://` 还是 `wss://`，默认只信 loopback 代理，非 loopback 需配 `daemon.trustedProxies`。Caddy 一行 `reverse_proxy 127.0.0.1:6767` 即可。Tailscale Serve / Cloudflare Tunnel 作为无公网 IP 的替代。

**7. 同源自动连接。** daemon 返回 `index.html` 时注入 `<script>window.__PASEO_INITIAL_DAEMON_CONNECTION__={listen:<Host头>,useTls:<https?>,label}</script>`（`web-ui.ts:253-275`），前端据此免去「Add Host」。静态资源免鉴权，API/WS 需鉴权。

**8. 镜像版本策略。** `ghcr.io/getpaseo/paseo:latest` 跟随 stable release，不是 main 构建（`public-docs/docker.md:13`）。

### 对我们的启示

1. **Go 服务端让镜像更简单**：多阶段 `golang:1.x` 编译静态二进制 + 前端 `node` 阶段 `vite build` → 运行阶段 `gcr.io/distroless/static` 或 `alpine`。前端产物 `embed` 进 Go 二进制，实现 Paseo 的「UI 版本永远与服务端一致、同源服务」。若 herdr 需要在容器里跑 tmux/shell/agent CLI，则运行阶段用 `debian-slim` + tini + gosu，照抄 Paseo 的 root 起步→修目录属主→降权模式。
2. **照抄的运维契约**：`HERDR_LISTEN=0.0.0.0:PORT`、`HERDR_HOME` 状态目录、两卷（state + workspace）、`/api/health` 免鉴权供 HEALTHCHECK、`HERDR_HOSTNAMES` / `HERDR_TRUSTED_PROXIES`、无密码/无管理员账号时启动告警。多用户版本应在首次启动**强制**初始化管理员（环境变量或 bootstrap token），而不是像 Paseo 只 warn。
3. **反代文档直接复用**：nginx 的 WS upgrade + 关缓冲 + 长超时 + 大 body + `X-Forwarded-Proto`；Go 侧读 `X-Forwarded-Proto` 时同样只信 trusted proxies。
4. **agent CLI 用子镜像**的做法与 herdr 一致（herdr 本身也是编排外部 CLI），基础镜像保持小且与第三方 CLI 发布节奏解耦。
5. **注入同源连接提示**（或更简单：前端默认 `location.host` 同源连 `/ws`）省掉配置步骤；Paseo 用 inline script 是因为同一份 UI 也部署在 app.paseo.sh 需要多 host。我们单一部署可直接同源。

---

## C. 直连与 VPN 路径

### Paseo 怎么做

**1. 直连就是普通 WebSocket。** `tcp://host:port?ssl=true&password=xxx` 是客户端侧的连接 URI 语法（`daemon-endpoints.ts:85-121`），实际拼成 `ws(s)://host:port/ws`（`:170-175`）。app 内叫 `directTcp` 连接，字段 `{endpoint, useTls, password}`（`host-connection-schema.ts:3-9`）。daemon 默认监听 `127.0.0.1:6767`，改 `daemon.listen`/`PASEO_LISTEN` 绑到 LAN IP、Tailscale IP 或 `0.0.0.0`（`public-docs/connectivity.md:86-100`）。

**2. TLS 处理：daemon 自己不做 TLS。** 只有 `useTls` 开关决定 `ws://` 还是 `wss://`；证书由反向代理 / Tailscale Serve / Cloudflare Tunnel 负责（`public-docs/web-ui.md:163-189`）。Tailscale 直连场景明确写「Use SSL 关掉」（`connectivity.md:119`），依赖 WireGuard 隧道加密。密码鉴权「只管访问控制，不加密流量」（`security.md:95-98`）。

**3. 一个 host 多条连接。** `HostProfile{serverId, connections: HostConnection[], preferredConnectionId}`（`packages/app/src/types/host-connection.ts:60-69`），同一 daemon 可同时有 relay、directTcp、remoteSsh 等多条连接，按 `serverId` 归并（「若已通过 relay 配对，直连会加到同一 host」，`connectivity.md:120`）。`serverId` 来自 `server_info`，是跨连接方式识别同一 daemon 的锚点。

**4. SSH 隧道（桌面/CLI）。** `ssh://user@host:port?daemonPort=7777`（`ssh-transport.ts:26-60`），用本机 OpenSSH 客户端非交互转发到远端 `127.0.0.1:6767`，不安装不启动 daemon（`connectivity.md:19-46`）。Electron 主进程持有 SSH 进程，渲染层通过一个 transport 边界访问（`architecture.md:96`）。移动端不支持 SSH。

**5. Unix socket / named pipe（CLI）。** `directSocket{path}` / `directPipe{path}`，最大隔离，仅本机进程（`security.md:61-63`；`host-connection.ts:23-33`）。

**6. 局域网发现：没有。** 仓库内没有 mDNS/Bonjour/zeroconf 实现（grep 无结果）。二维码 offer 里也不含 LAN 地址（v2 是 relay-only）。`buildOfferEndpoints` 只用于把 LAN IP 列进 `/api/status` 之类的展示（`connection-offer.ts:10-28`），用户在手机上手输 IP:端口。

### 对我们的启示

1. **我们的默认路径就是「直连 + 反代 TLS」**，与 Paseo 的 Docker/自托管路径一致。Go 服务端不必自己终止 TLS；但为 homelab 用户提供 `--tls-cert/--tls-key` 或自动 Let's Encrypt（`golang.org/x/crypto/acme/autocert`）是加分项，Paseo 缺这个。
2. **多连接归并到同一 serverId**值得借鉴：手机上同一个 herdr 服务端可以有「公网域名」「Tailscale IP」「LAN IP」多条入口，按 `server_info.serverId` 归并，按可达性自动选优。二维码里可以一次带多个 endpoint（Paseo v1 offer 曾这么做，v2 收敛为 relay-only）。
3. **VPN 路径零改动**：Tailscale/WireGuard 下就是 `ws://100.x.y.z:port`，只需允许用户关掉「强制 HTTPS」并在 UI 上提示风险。
4. **局域网发现可选做**：mDNS 广播 `_herdr._tcp` 让手机 App/PWA 自动发现同网段服务端，但浏览器无法直接用 mDNS，只有原生 App 或「二维码带 LAN IP」可行。低优先级。

---

## H. 技术栈盘点与适用性

### Paseo 怎么做

| 层 | 选型 | 证据 |
| --- | --- | --- |
| 跨端框架 | Expo SDK 54 + React Native 0.81 + react-native-web 0.21，一份代码出 iOS/Android/Web/Electron | `packages/app/package.json:78, 113, 126` |
| 路由 | expo-router 6（文件路由 `app/h/[serverId]/workspace/[workspaceId]`、`agent/[agentId]`、`pair-scan`） | `packages/app/src/app/` 目录 |
| 状态 | zustand 5（十余个领域 store）+ TanStack Query 5（目录支撑的服务端数据缓存，键 `(serverId, cwd)`）+ `use-sync-external-store` | `package.json:135, 66`；`docs/data-model.md:527-532` |
| 样式 | react-native-unistyles 3（Babel 插件、主题在原生层更新、禁止 `useUnistyles()` 钩子） | `docs/unistyles.md` |
| 设计系统 | 自建 primitives（`components/ui/`），token 集中在 `styles/theme.ts`，14px 基准、权重表意 | `docs/design.md` |
| 编辑器/终端 | CodeMirror 6（含 vim 模式）、xterm.js 6、markdown-it、mermaid、Skia | `package.json:42-46, 64, 68-76, 106-107` |
| 手势/动画 | react-native-gesture-handler、reanimated 4、keyboard-controller、bottom-sheet | `package.json:56, 116-120` |
| 本地存储 | AsyncStorage（偏好、草稿）、expo-sqlite / IndexedDB（replica cache） | `data-model.md:523-552` |
| 协议 | zod 4 schema 单一来源，server/client/app 共享；入站校验器由 codegen 生成 | `packages/protocol`，`docs/protocol-validation.md` |
| 服务端 | Node 22 + Express + `ws` + pino + bcryptjs + node-pty + tweetnacl | `packages/server` |
| 构建 | npm workspaces、tsc、`@typescript/native-preview`(tsgo) 做 typecheck、oxlint/oxfmt、knip 查死代码、lefthook | 根 `package.json` |
| 测试 | vitest（含 `@vitest/browser` 真浏览器单测）、Playwright（Web e2e、性能 spec）、Maestro（移动 e2e）、TDD 纪律与「可失败用户操作必须有 UI 反馈」规范 | `docs/testing.md` |
| 部署 | Web 前端 → Cloudflare Pages（`deploy:web`）；daemon 内嵌 Web UI；Docker 镜像 → ghcr | `package.json:37`，`docker/` |
| i18n | i18next + react-i18next | `package.json:104, 112` |

### 对我们的启示

**可以借鉴的：**
1. **protocol 包单一来源 + 服务端不依赖前端**。Go 后端下等价做法：用 Go struct + `encoding/json` 作为权威，通过 `tygo` / `go2ts` 或 OpenAPI/JSON Schema 生成 TS 类型与 zod schema；或反过来以 JSON Schema 为源同时生 Go 与 TS。关键是「一处定义、两端校验、新字段只加 optional」。
2. **zustand + TanStack Query 的分工**：UI/布局/草稿状态进 zustand；服务端数据按 `(serverId, resourceKey)` 进 Query 缓存，WS 事件到达时 `setQueryData`/`invalidate`。这套在纯 React Web 里更顺手。
3. **文件路由按 host 前缀组织**（`/h/[serverId]/...`）让多服务端/多用户切换只是 URL 变化，深链和刷新都可恢复。我们的 URL 可设计为 `/s/[serverId]/w/[workspaceId]/t/[tabId]`。
4. **设计纪律**：primitives 集中、token 集中、权重表意而非字号；移动端三面板（列表/主体/侧栏）单一归一化位置的手势模型（`docs/mobile-panels.md`）对 PWA 的抽屉实现同样适用。
5. **测试纪律**：真浏览器跑单测、Playwright 覆盖「成功 + 失败」两条路径、性能 spec 有基线数字。
6. **oxlint/oxfmt + knip** 组合快且能清死代码，前端工程可直接采用。

**因为我们用 Go 后端而不适用或需替换的：**
1. **Expo / React Native Web**：我们不出原生 App，直接用 Vite + React（或 Next/TanStack Start）+ PWA，避免 RN 的抽象税（Paseo 大量文档在讲 Unistyles/Reanimated/Fabric 的坑）。移动适配靠响应式 + PWA manifest + Web Push。
2. **node-pty / worker 进程 / IPC 合并器**：Go 里用 `creack/pty` + goroutine 直接写 WS，无需 IPC 层，但要保留「5ms 合并、背压门控快照」两个策略。
3. **tweetnacl 主线程加密**：Go 的 `nacl/box` 是原生实现，性能不是问题；前端若做 E2EE 用 libsodium.js（WASM）比 tweetnacl 快。
4. **单进程内存 Session**：Go 服务端若要多副本，需外置会话/seq（Redis）或 sticky routing；Paseo 从未考虑这点。
5. **Expo Push**：替换为 Web Push（VAPID）+（可选）原生封装时的 FCM/APNs。
6. **Electron 桌面壳、SSH transport、Unix socket 连接**：Web 版不需要。

---

## 附：对 herdr Web 客户端的综合建议（按优先级）

1. **协议层**（G）：两层 envelope、强制 hello 15s、4xxx 关闭码、`requestId` 关联 + `rpc_error`、`server_info.features` 能力位、COMPAT 标签纪律。
2. **认证与配对**（A/D）：用户 + 密码/OIDC 登录签发 session token；设备配对 = 服务端生成一次性 `pairingToken`（5 分钟有效）→ 二维码 `https://herdr.example/#pair=<base64url{v,serverId,endpoint,pairingToken,expiresAt}>` → 手机换取长期 `deviceToken` → WS 用 `Sec-WebSocket-Protocol: herdr.bearer.<deviceToken>`；服务端维护设备表支持单设备撤销。
3. **同步模型**（G）：目录用 `generation + 单调 seq + tombstone` 增量；终端/日志流用 `[opcode][slot][bytes]` 二进制帧；会话按 `(userId, clientId)` 保留 90s。
4. **终端**（E）：xterm.js + 修饰键工具栏 + `visualViewport` 键盘避让 + resize claim/update。
5. **部署**（F/C）：单二进制 embed 前端、distroless 或 slim 镜像、两卷、`/api/health`、反代 WS 模板、`X-Forwarded-Proto` 只信 trusted proxies、首启强制管理员初始化。
6. **relay**（B）：暂不做；若未来支持「云端前端 → 用户本机 daemon」再按 v2 协议 + NaCl box 在 Go 里实现。
