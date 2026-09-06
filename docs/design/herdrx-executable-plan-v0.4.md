# herdrx 可执行方案 v0.4（已批准）

> 日期：2026-09-03
>
> 状态：2026-09-04 用户确认全部按推荐执行；进入连续交付阶段
>
> 输入：`herdrx-plan-v0.3.md`、`review-codex.md` 与 `research/` 六份一手材料

## 0. 一句话方案

herdrx v1 是一个**可信实例管理员、单实例、多用户、小团队规模**的 Web 客户端：浏览器通过同源 HTTPS/WSS 连接 Go 服务端；服务端用基础 SSH 或 tailcat+受限 agent 连接用户主机；结构与状态来自 herdr JSON API，终端来自 `terminal session observe/control`；Web 只镜像 herdr 的 BSP 拓扑与交互语义，不成为新的会话服务端。

## 1. 开工闸门

只有用户明确确认以下基线后，才开始 S0 技术验证；确认 S0 的 ADR 后，才开始 M1 产品代码。

### 1.1 默认决策基线

| 决策 | v0.4 默认值 |
|---|---|
| 信任模型 | 实例与实例管理员可信；不承诺防恶意运营方，不做端到端加密 |
| 部署形态 | v1 单实例，不支持 HA/多副本；面向个人和互相信任的小团队 |
| 注册 | 首次空数据库初始化管理员；默认关闭注册，由管理员手动开启邀请注册；开放注册不进入 v1 |
| SSH 凭据 | 每主机生成独立 ed25519 key；可选上传私钥或显式保存密码 |
| tailcat 配对 | agent 安装时预置该 host 的 tailcat/SSH 公钥；agent 展示一次性 QR；服务端主动拨号 |
| DERP | 生产推荐自建；作为显式 compose profile，不无条件占用 80/443/3478；公共 relay 仅试用 |
| 手机入口 | 主机内先到 agents/attention 列表，同时显示“继续上次终端” |
| 布局 | 忠实 BSP 拓扑、顺序、ratio、zoom；像素按浏览器响应式计算；不做自由布局 |
| TUI 直出 | S0 做 direct SSH 诊断探针；M2 再做正式、含 agent 路径的功能 |
| 主题 | Cobalt2 默认 + 6 个内置；状态同时用颜色、图标和文字；提供增强对比模式 |
| 仓库 | Go 1.27 单模块 `github.com/riba2534/herdrx`；`cmd/herdrx`、`cmd/herdrx-agent`、`internal`、`web`、`deploy`、`docs` |
| 共享主机 | v1 不做；每台 host 有唯一 owner |

### 1.2 暂定产品容量，不作为未经测试的承诺

v1 先按以下“小团队单实例”画像设计，最终数字由 S0 基准修订：

- 1–10 个用户；
- 每用户最多 50 台已配置主机；
- 默认每实例最多 20 台主机启用持续后台状态监控；
- 每用户最多 16 个同时可见终端流；
- 每个浏览器连接最多 32 个终端流；
- 单实例不做跨进程 session 迁移。

如果用户目标明显超过这个范围，应在 S0 前直接重新评估 tailcat 拓扑 B 和外置数据库，而不是先按拓扑 A 实现后再迁移。

### 1.3 不进入 v1 的内容

- 多用户共享同一主机、RBAC 和协同输入；
- 多副本/HA、Redis/Postgres、跨实例 WS 恢复；
- 浏览器 tailcat WASM 直连；
- bincode client-owned shell；
- 完整 OpenSSH alias/ProxyJump/ProxyCommand/FIDO/GSSAPI 兼容；
- 不信任 herdrx 运营方时的端到端加密；
- 自由 pane 布局、原生 iOS/Android App。

## 2. 固化后的系统架构

```text
Browser/PWA
  ├─ REST: auth / hosts / settings / devices
  └─ WSS: projection events / requests / terminal streams / presence
          │
          ▼
herdrx-server（Go 1.27，单实例）
  ├─ Auth + SQLite + encrypted secrets
  ├─ HostRuntime Registry（按 owner_id + host_id）
  │    ├─ Monitor：snapshot/events、状态投影、通知
  │    ├─ SSHTransport：direct TCP 或 tailcat net.Conn
  │    └─ TerminalStream：observe/control exec channel
  ├─ Control Lease Registry（按 host/session/pane）
  ├─ WS Resume Sessions（按 user/browser_instance）
  └─ Web Push
          │
          ├─ direct SSH ── sshd ── herdr.sock / herdr CLI
          └─ tailcat ── herdrx-agent 的受限 SSH ── herdr.sock / herdr CLI
```

### 2.1 服务端内部边界

不把所有接入方式写死成 `*ssh.Client`。使用以下能力边界：

```text
Transport
  Dial(ctx) -> net.Conn                     # direct TCP 或 tailcat TCP

SSHPeer
  Exec(ctx, argv, io)                       # 固定、受校验的远端命令
  DialStreamLocal(ctx, absoluteSocketPath)  # Unix socket
  KeepAlive(ctx)
  Close()

HerdrEndpoint
  Snapshot(ctx)
  Subscribe(ctx, desiredSubscriptions)
  Call(ctx, allowedMethod, validatedParams)
  OpenTerminal(ctx, pane, mode, size, takeover)
```

基础 SSH v1 只承诺 host/port/user、生成 key、OpenSSH 私钥和密码。未来若加入系统 OpenSSH/ProxyJump，不影响上层 HerdrEndpoint。

### 2.2 HostRuntime 生命周期

每个 host 最多一个共享 HostRuntime；浏览器标签页不各建一套 SSH/tailcat 连接。

```text
dormant
  └─ 用户打开或启用通知 → connecting
connecting
  ├─ 成功 → monitoring
  └─ 失败 → retry_fast → retry_slow（带 jitter，永不永久放弃）
monitoring
  ├─ 只保持 request/events；终端按需开
  ├─ 无浏览器且未启用通知 → idle grace → dormant
  └─ 链路失败 → reconnecting → snapshot resync → monitoring
```

- 启用 Web Push 的主机允许常驻 monitor；否则最后一个浏览器离开 2 分钟后断开。
- 前台重连：1/2/5/5/10/10/30 秒；后台慢速探测逐步到 5 分钟上限，增加 ±20% jitter。
- 页面重新可见、用户点击重连或网络恢复时立即打断慢速等待。
- tailcat 用 `DiscoPing` 探活；任一侧重启导致失败时 Close 旧 Client 并创建新对象。

### 2.3 herdr 状态引导

S0 必须验证后才把算法标为最终；候选算法如下：

1. 打开事件通道并缓冲。
2. 获取 `session.snapshot`。
3. 仅对具备可比较 revision、且 revision 新于 snapshot 的事件做 reducer。
4. 无可靠 revision 的结构事件只标记 dirty，并触发节流 fresh snapshot。
5. 新 pane 出现时更新 desired subscriptions；切换订阅连接期间仍以 snapshot 收敛。
6. herdrx 将已验证的投影变更转换为自身 `generation + seq + tombstone`。
7. 事件通道重连后强制 snapshot，不依赖猜测丢失范围。

前端不消费 herdr 原始事件，只消费 herdrx 投影。

## 3. 浏览器协议 v1 草案

### 3.1 三种身份

- `device_id`：随机 128 bit，localStorage，表示一台浏览器设备；不作为鉴权凭据。
- `browser_instance_id`：随机 128 bit，sessionStorage，每个标签页唯一。
- `connection_id`：每次 WS 由服务端生成。

认证只使用 HttpOnly session Cookie；v1 前端与服务端同源，WS 必须校验 Cookie 与 Origin。`client_id` 不再同时承担多种职责。

### 3.2 hello 与恢复

浏览器升级后 15 秒内发送：

```json
{
  "t": "hello",
  "protocol": 1,
  "device_id": "...",
  "browser_instance_id": "...",
  "client_type": "browser",
  "resume": {
    "session_id": "...",
    "generation": "...",
    "after_event_seq": 42
  },
  "capabilities": {
    "terminal_ack": 1,
    "web_push": true
  }
}
```

- 一个 browser instance 同时只允许一个活动 socket；恢复连接替换旧连接。
- 服务端保留断线 session 90 秒，期间保留订阅描述和终端 stream 对象。
- generation 不匹配、cursor 过期或进程重启时返回全量投影。
- 请求/响应/事件继续用 JSON text frame；方法必须来自显式 allowlist。

### 3.3 终端二进制帧

固定 16 字节小端头：

```text
byte 0      kind = 0x74
byte 1      protocol version = 1
byte 2      opcode
byte 3      flags（FULL/FINAL 等）
byte 4..7   stream_id u32
byte 8..15  seq u64
byte 16..   payload
```

`stream_id` 在一个 resume session 内永不复用；open JSON 响应另带随机 `stream_epoch`。新 epoch 必须先收到 full frame，客户端才接受增量。

| opcode | 方向 | 含义 |
|---|---|---|
| 1 FRAME | server→client | payload 固定为 `[cols u16][rows u16][ANSI bytes]`；`FULL` flag 表示客户端先 reset |
| 2 ACK | client→server | header 的 seq 是已由 xterm `write` callback 解析的累计确认值；无 payload |
| 3 INPUT | client→server | 原始输入字节；服务端按 connection/stream 校验租约 owner |
| 4 RESIZE | client→server | payload 固定为 `[cols u16][rows u16]`；仅 control owner 可发 |
| 5 SCROLL | client→server | payload 固定为 `[direction u8][source u8][lines u16]` |
| 6 RELEASE | client→server | 主动释放控制租约；无 payload |
| 7 RESIZE_ACK | server→client | payload 固定为 `[cols u16][rows u16]` |

流控初始值先采用经参考项目验证过的保守值，S0 用实测修订：

- 输出合并窗口 5 ms；单 chunk 48 KiB；
- 每流初始未确认窗口 512 KiB；
- 每流硬队列 2 MiB；每连接硬队列 8 MiB；
- 超限不继续积压：丢弃待发增量、标 dirty、请求/重开 full snapshot；
- 浏览器每累计 192 KiB 或 4 ms 发送累计 ACK；
- 每个输入/resize/scroll 仍做服务端租约与参数校验。

### 3.4 控制租约

租约主键：`host_id + herdr_session + pane_id`。租约 owner：`user_id + browser_instance_id + stream_id`。

- 进入 pane 只开 observe，不改变 PTY。
- 用户明确点击“接管”或首次输入时申请 control；若 herdr 报已有 controller，显示确认，默认不自动 `--takeover`。
- acquire 期间最多缓存 32 KiB 输入、最长 2 秒；失败则不发送并给出明确提示。
- 服务端 control 成功并收到第一帧 full snapshot 后，才确认租约和发送缓存输入。
- 活跃页面每 5 秒续租，租约 20 秒；WS 进入 90 秒恢复期时，控制租约只保留 10 秒，之后主动 release，避免冻结手机尺寸。
- 页面 blur 只触发尽快 release，不作为唯一保证；服务端 TTL 才是权威。
- lease token 不匹配的 INPUT/RESIZE/SCROLL 一律拒绝。
- Web 本地选择 workspace/tab/pane 不自动调用 herdr focus；seen 写回方式由 S0 双客户端实验决定。

## 4. 安全与数据基线

### 4.1 账号与会话

- 首启生成一次性 bootstrap token，只显示在服务端终端/受限文件；初始化管理员后销毁。
- argon2id 参数在首次实现时以目标机器约 250 ms 验证耗时校准，并把参数随 hash 保存。
- session token 使用 256 bit 随机 opaque token，数据库只存 hash；支持单会话注销、全部注销、禁用用户。
- Cookie：Secure、HttpOnly、SameSite=Lax；登录后轮换 session；REST 写操作校验 CSRF；WS 校验 Origin。
- invite code 只存 hash，带 expires_at，事务内一次性消费。

### 4.2 凭据

- 每 host 单独生成 SSH key；不复用用户级 SSH private key。
- secret envelope 从 v1 带 `version/key_id/nonce/ciphertext`；AAD 绑定 `user_id/credential_id/kind`。
- 已有密文但 master key 缺失时拒绝启动；不静默生成新 key。
- 备份必须同时包含 SQLite 与 master key；提供备份校验和恢复演练。
- 上传 key、保存密码、接受 host key、轮换和删除都写安全审计，不记录秘密本身。

### 4.3 SSH 与 API 权限

- direct SSH 首次连接显示 SHA-256 host key fingerprint；记录完整 key 类型和 public key，而不仅是展示字符串。
- tailcat QR 包含 tailcat 长地址、agent SSH host key/fingerprint、一次性 token、版本和过期时间。
- herdr JSON method 使用显式 allowlist：M1 仅开放 snapshot、所需事件、工作台 P0 动作和必要读取；危险方法逐个评审。
- herdrx-agent 不启动 shell；只处理严格定义的 exec argv、PTY TUI 子命令和精确 streamlocal 请求。
- 所有 cols/rows、pane/session ID、输入长度、请求并发、frame 大小都有上限。

### 4.4 网络与租户隔离

- 所有 store 查询必须以 `user_id`/owner_id 作为必选条件，并有跨租户测试。
- 默认每用户 host、WS、SSH、tailcat、terminal、in-flight request 和 pairing rate limit。
- host DNS 解析后再检查 egress policy，阻止云 metadata/明确保留地址；是否允许私网由实例管理员配置。
- 终端输出视为不可信：CSP、http/https URL scheme allowlist、OSC 52 默认关闭、标题长度限制、图片协议默认关闭。
- M1 审计：登录/失败、用户禁用、邀请、凭据、host key、配对/撤销、takeover、破坏性 API。终端输入输出永不落审计。

## 5. agent 与 DERP 的可执行设计

### 5.1 agent 运行身份

- 一份 agent 对应一个 OS 用户；以同一 UID 访问 herdr。
- Linux 默认 `systemd --user`，macOS 默认用户 LaunchAgent；不以 root 长驻。
- agent 启动时解析实际 HOME/XDG 配置，支持默认与命名 herdr session。
- herdr daemon 未运行时，通过固定、无用户输入的受限命令确保启动，再定位 socket。

### 5.2 安装与升级

正式安装链路在 M2 完成：

1. Web 为具体 host 生成一次性安装记录和每 host 公钥。
2. 安装器探测 OS/arch，下载固定版本二进制。
3. 校验 SHA-256 和发布签名后原子替换。
4. 写 0600 配置、安装用户级服务、启动并输出 status。
5. `pair` 本机生成 128 bit token，10 分钟过期，打印 QR/链接。
6. 配对完成后 token 原子失效；重复扫码返回已使用。

必须同时提供 `status`、`logs`、`update --check`、`unpair`、`uninstall`。服务端与 agent 发布同一版本号，并声明兼容范围。

### 5.3 DERP 部署

- `deploy/compose.yml`：herdrx + 可选 caddy，不强制 derper。
- `deploy/compose.derp.yml` 或 profile `derp`：独立 derper；要求单独 DERP 域名与 3478/udp。
- 文档给出两套完整示例：Caddy 终止 app HTTPS + derper 自有证书；或统一反代但 STUN 仍直接暴露。
- 长地址固定 region；更换 DERP 域名/region 触发 agent 重新生成地址并在 UI 标记需要重新配对。
- 公共 `tailcat.dev` 只能在显式 `trial` 配置下使用，UI 显示无 SLA/可能限速。

## 6. 分阶段执行计划

### G0：拍板与文档冻结（0.5 天，不写产品代码）

#### 任务

- G0.1 用户确认 1.1 的默认决策，特别是信任模型、单实例、注册、每 host key、DERP packaging。
- G0.2 将本方案状态从“待拍板”改为“Approved”，记录批准日期。
- G0.3 建立 ADR 索引，首批 ADR：接入协议、终端 wire、控制租约、tailcat 拓扑、信任模型。

#### 完成标准

- 所有未决策项只有明确 owner 和期限，不存在会改变 S0 技术路线的开放问题。
- 用户明确说可以开始 S0。

### S0：协议与网络技术验证（5–7 个工作日，允许丢弃代码）

S0 只回答高风险问题，不做登录页、CRUD、设计系统或生产数据库。

#### S0.1 只读协议探针（依赖：G0）

产物：`spike/` 下的临时 CLI/harness、原始 trace、结论记录。

- direct SSH 获取远端绝对 HOME/XDG/session socket；验证 streamlocal。
- 验证 herdr 未启动、默认 session、命名 session、streamlocal 被禁用时的行为。
- 记录 snapshot、全部目标事件和 terminal frame 的真实 JSON schema。
- 验证 JSON request 是否支持并发、响应是否可能乱序、超时后连接是否可继续用。

通过：两台测试主机都可重复运行，失败路径有稳定错误码，不依赖人工看日志猜测。

#### S0.2 状态一致性（依赖：S0.1）

- 订阅→缓冲→snapshot→回放；记录每类事件是否有可靠 revision。
- 新建/移动/关闭 workspace/tab/pane，动态新增 agent 状态订阅。
- 人为断开事件连接，在 gap 中改变结构，再重连收敛。

通过：同一 trace 随机重复、重排允许重排的部分后，最终投影始终等于 fresh snapshot；无法证明的事件明确改成 snapshot invalidation。

#### S0.3 终端正确性（依赖：S0.1）

- 把 full/incremental frame 喂入 xterm；验证 reset 时机。
- 主动丢一帧、重复一帧、延迟帧、断开 WS，再请求 full。
- 场景：zsh、快速彩色输出、vim/htop、Claude/Codex TUI、alt-screen、mouse reporting、CJK/emoji、bracketed paste。
- 实现最小 Ack 或 bounded-drop 原型，测慢消费者内存。

通过：任何 gap 最终能恢复，不永久花屏；服务端排队达到上限后内存保持有界；普通输入 p95 以测试环境记录而非凭感觉判断。

#### S0.4 多客户端与控制权（依赖：S0.3）

- 原生 herdr TUI + 桌面浏览器 + 手机浏览器同时看同一 pane。
- observe 裁剪、control resize、takeover、release、浏览器崩溃、手机后台、first-key 缓存。
- 验证 JSON focus 与 seen 是否影响其他客户端。

通过：20 秒租约过期能可靠 release；非 owner 输入被拒绝；没有 resize 循环；takeover 一定有可见确认；Done/seen 形成书面结论。

#### S0.5 tailcat/agent 最小链路（依赖：S0.1，可与 S0.2–S0.4 并行）

- agent 以非 root 同一 OS 用户运行，只开放受限 SSH 与精确 herdr socket。
- 固定 region、长地址、预置白名单、SSH host key pin、一次性 token。
- agent 重启、服务端 Client 重建、DERP-only、直连升级、换网恢复。
- 启动 1/10/20/50 个 Client，记录 RSS、CPU、goroutine、fd、DERP 连接和首连延迟。

通过：命令注入与越界 socket 测试全部拒绝；重启后不被 `Ping()` 假成功迷惑；容量数据足以决定拓扑 A 是否继续。

#### S0.6 恢复矩阵与 ADR（依赖：S0.2–S0.5）

产出 `docs/design/adr/` 下至少五份记录和一份 S0 报告：

- browser WS 断线；
- server↔host SSH 断线；
- tailcat 任一侧重启；
- herdr daemon 重启；
- herdrx-server 重启。

每种写清：进程是否存活、当前屏是否收敛、历史是否完整、控制权何时释放、用户看到什么。

#### S0 止损条件

出现任一条件就暂停 M1，先改架构：

- JSON focus/seen 会不可接受地扰动原生 herdr，且无本地状态替代路径；
- terminal incremental frame 无法检测/恢复 gap；
- 常见 sshd 无法 streamlocal 且没有可接受 bridge；
- 20 个持续在线 tailcat Client 已超过目标部署资源预算；
- control/takeover 无法避免手机与桌面 resize 战争；
- agent 无法以普通用户稳定访问同一 herdr session。

### M1：单实例桌面 MVP（S0 ADR 通过后，3–4 周）

#### M1.1 工程与协议基线（2–3 天）

- Go root module、两个 cmd 入口、`internal` 分层、web workspace。
- JSON/二进制协议单一来源与 Go↔TS golden tests。
- CI：Go test/vet、前端 typecheck/lint/unit、构建镜像、最小 Playwright smoke。
- 版本、build info、migration、feature flags、COMPAT 纪律。

完成标准：空服务、前端和镜像可重复构建；协议 golden test 在 CI 中阻止漂移。

#### M1.2 Auth、SQLite 与 secrets（3–4 天，依赖 M1.1）

- bootstrap 管理员、登录/注销/会话列表、邀请、用户禁用。
- SQLite schema、WAL/foreign_keys/busy_timeout、migration、备份原语。
- versioned AES-GCM envelope、每 host credential、审计基线。
- CSRF、Origin、限速、owner_id 查询约束与跨租户测试。

完成标准：抢注路径关闭；数据库和 master key 的备份恢复演练成功；跨用户 ID 猜测不能读取或操作他人资源。

#### M1.3 direct SSH HostRuntime（3–4 天，依赖 M1.2、S0 ADR）

- SSH host CRUD、每 host key、上传 key/密码、host fingerprint 确认与变化阻断。
- socket locator、request/events 双连接、重连监督、版本/capability 检查。
- 显式 herdr method allowlist、超时、取消、并发上限。

完成标准：direct SSH 主机可后台监控，链路失败后自动恢复；host key 变化绝不静默接受。

#### M1.4 WS projection 与 terminal protocol（4–5 天，依赖 M1.1、M1.3）

- hello/resume、browser instance identity、generation/seq/tombstone。
- 16-byte terminal frame、Ack/credit、有界丢帧与 full recovery。
- control lease、first-key buffer、takeover confirm、presence。
- 每连接/用户配额、close codes、半开探测。

完成标准：S0 故障用例转成自动集成测试；8 小时持续输出/断网 soak 不出现无限内存增长和永久花屏。

#### M1.5 桌面工作台（5–7 天，依赖 M1.4）

- hosts、workspace sidebar、agents、tabs、稳定 xterm host、BSP pane。
- Cobalt2、WebGL 自动降级、fit 稳定帧、拖动时仅本地 fit、松手回写。
- prefix 核心键、focus/swap/split/resize/zoom/close、右键菜单。
- observe/control 明示、连接覆盖层、错误可恢复提示。

完成标准：日常 direct SSH 开发主路径可用；React 重渲染不销毁 xterm；浏览器刷新能恢复到同一位置和当前屏。

#### M1.6 部署与发布门（3–4 天，可与 M1.5 后半并行）

- 单镜像 embed 前端；非 root；health/readiness；可选 Caddy。
- CSP、trusted proxies、日志脱敏、metrics、资源上限。
- SQLite+master key 备份/恢复文档、升级/回滚、数据卷权限。
- Chrome/Firefox/Safari 桌面 E2E；Linux amd64/arm64 server 构建。

M1 发布标准：

- 所有 M1 自动测试通过；
- direct SSH 两台主机连续使用 3 天无 blocker；
- 高危安全项为 0；
- 安装、初始化、备份、恢复、升级均由未参与开发者按文档完成一次；
- 明确标注 single-instance、trusted-admin、基础 SSH 能力边界。

### M2：tailcat、手机与通知（3–4 周）

#### M2.1 生产 agent 与配对（5–7 天）

- Linux/macOS amd64/arm64 构建与签名；用户级服务。
- install/status/logs/update/unpair/uninstall。
- tailcat supervisor、受限 SSH、host key pin、token 原子消费。
- 在线撤销关闭活跃连接；离线撤销状态与本机 unpair。

完成标准：全新 NAT 后主机 1 分钟内完成安装+扫码+连接；泄漏已使用/过期 QR 不可重新配对。

#### M2.2 手机工作台（5–7 天，依赖 M1.5）

- agents/attention 首页、继续最近 pane、两行 header、switcher。
- 虚拟键条、sticky Ctrl/Alt/Shift、直接输入/缓冲输入、IME。
- `visualViewport` 键盘避让、手势、选择/粘贴、回到底部。
- 只有明确输入/接管才 control；后台冻结后由租约自动释放。

完成标准：iOS Safari 与 Android Chrome 完成 blocked agent 回复；键盘弹出/旋转/切后台不造成永久尺寸锁。

#### M2.3 PWA、Web Push 与 DERP profile（4–5 天）

- manifest、版本化 service worker、安全更新流程。
- VAPID、push subscription 租约、410 自动清理、presence 抑制重复通知。
- 自建 DERP profile、公共 trial、直连/DERP 路径展示。

完成标准：页面全关后 blocked/done 仍能推送；点击通知定位正确 pane；旧前端不会在服务端升级后无限重连。

#### M2.4 正式 TUI 直出与兼容回归（2–3 天）

- direct SSH 与 agent 都实现固定 `herdr` PTY command、window-change、signal、exit。
- 与 Web 自绘 terminal 共用控制租约，不允许双 owner。
- 作为诊断/兜底入口，不替代默认工作台。

M2 发布标准：

- direct、P2P 直连、DERP-only、Wi-Fi↔蜂窝切换全部通过；
- agent 重启与服务端重启能恢复；
- 20 个后台 monitor 的资源数据不超过 S0 确认预算；
- 手机 30 秒内完成收到通知→打开→回复的目标，在可控网络条件下有实测记录。

### M3：增强（按用户价值逐项排期）

- worktree UI；
- agent prompt/wait/send_keys 快捷操作；
- scrollback 搜索/导出与图片粘贴桥；
- TOTP/Passkey 与恢复码；
- 更完整审计和安全告警；
- 自定义主题、键位导入；
- popup/plugin pane；
- 多命名 herdr session UI；
- 拓扑 B/HA 预研（只有容量数据要求时启动）。

## 7. 测试矩阵

| 维度 | S0 | M1 | M2 |
|---|---|---|---|
| herdr | 当前稳定版；记录 protocol/schema | 最低支持版 + 当前稳定版 | 再加升级前后兼容 |
| 主机 | Linux direct；Linux tailcat | Linux amd64/arm64 direct | Linux/macOS amd64/arm64 agent |
| 浏览器 | Chrome 桌面 + 手机模拟 | Chrome/Firefox/Safari 桌面 | iOS Safari、Android Chrome、已安装 PWA |
| 网络 | LAN、强制 DERP、断 SSH | WS 半开、反代重启、主机休眠 | Wi-Fi/蜂窝切换、DERP 重启、agent 重启 |
| 终端 | shell/TUI/CJK/大输出 | prefix/BSP/scroll/copy/paste | IME/软键盘/手势/后台恢复 |
| 多客户端 | native TUI + 2 browser | 两标签页抢租约 | 桌面+手机+通知 |
| 安全 | 命令/路径越界 | auth/CSRF/Origin/SSRF/tenant | pairing replay/revoke/update signature |

每个可失败的用户操作必须同时有成功测试、失败测试和可理解的 UI 错误；不能只在日志里报错。

## 8. 里程碑产物与汇报格式

每个里程碑结束只汇报四类事实：

1. **交付物**：代码/文档/镜像/测试所在路径与版本；
2. **证据**：通过的自动测试、真实设备场景、性能数字；
3. **偏差**：与本方案不同的地方及原因；
4. **下一闸门**：需要用户确认的单一决策。

严禁用“基本可用”“应该没问题”“不丢内容”这类无法验收的表述代替证据。

## 9. 用户拍板后的第一周顺序

```text
Day 0.5  冻结决策与 ADR 目录
Day 1    direct SSH + 绝对 socket locator + snapshot/events trace
Day 2    terminal full/incremental/gap harness + xterm correctness
Day 3    Ack/有界队列原型 + WS 断线恢复
Day 4    多客户端 control lease + focus/seen 实验
Day 5    tailcat 非 root agent 最小链路 + 重启恢复
Day 6    1/10/20/50 Client 容量基准 + DERP-only/换网
Day 7    故障矩阵、ADR、Go/No-Go 评审
```

Day 7 的 Go 条件不是“demo 能打字”，而是 R2–R11 的协议和控制问题已经有实测结论，且 tailcat 拓扑 A 的容量数据符合 v1 目标。若不符合，先修订架构，不进入 M1。
