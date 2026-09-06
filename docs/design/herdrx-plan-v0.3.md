# herdrx 产品方案与技术架构（v0.3 调研稿）

> 状态：调研完成，待评审拍板。日期：2026-09-03。v0.2 合并 tailcat / paseo / orca 专项报告；v0.3 合并 herdr wire 协议与交互模型的最终报告（详见 `research/`）。
> 依据：herdr 0.8.2 源码与本机实测、tailcat 库源码、orca / paseo 参考实现。所有协议结论均为一手验证，非推测。

## 0. 定位

**herdrx 是 herdr 的多用户 Web 客户端**：部署在任意位置的 Go 服务 + 浅色风格的现代 Web 前端（电脑与手机同一套代码），把 `herdr --remote` 的能力搬到浏览器里，并用 tailcat 打通任意网络环境的机器。

- 用户注册登录后，添加自己的 herdr 主机（普通 SSH，或装一个 agent 走 tailcat P2P 打洞）。
- 进入主机后得到一个"原汁原味"的 herdr 工作台：workspace / tab / pane 三层结构、agent 状态（blocked / working / done / idle）、prefix 快捷键、分屏、鼠标操作，终端用 xterm.js 渲染，默认 Cobalt2 主题。
- 手机端沿用 herdr 自带窄屏模式的信息架构（agents 优先 → spaces → tabs → menu），加虚拟键条与手势。

非目标（v1 不做）：替代 herdr 服务端、在浏览器里跑 herdr 二进制、agent 编排逻辑（交给 herdr 本身与 skill）、多用户共享同一主机的权限模型。

## 1. 调研结论摘要

### 1.1 herdr 远程连接的真相
`herdr --remote <ssh-target>` = 本地 thin client + `ssh target "herdr remote-client-bridge"` + 远端 unix socket 桥接。传输层就是 OpenSSH，认证就是 SSH 认证，远端必须有 herdr 二进制。所以 **herdrx 用 SSH 协议访问主机是与官方完全一致的路径**，不是绕道。

### 1.2 第三方客户端的官方 JSON 路径（已实测）
herdr 在同一台机上暴露两个 socket：`herdr.sock`（NDJSON API，91 个方法）和 `herdr-client.sock`（bincode 渲染协议）。官方文档明确为"third-party bridges"提供了纯 JSON 路径：

| 需求 | 官方手段 | 实测 |
|---|---|---|
| 结构（workspace/tab/pane/agent/布局） | `session.snapshot` | 一次拿全，含每个 tab 的 pane 矩形与 split 树 |
| 实时变化 | `events.subscribe`（27 种事件） | `layout_updated` 直接带完整布局；订阅时会回放历史事件，需按快照幂等 |
| 控制 | `pane.split / zoom / focus_direction / resize / swap / close / rename`、`tab.*`、`workspace.*`、`layout.set_split_ratio`、`agent.prompt / wait` 等 | 与 TUI 键位一一对应 |
| 终端内容 | `herdr terminal session observe\|control <pane> --cols --rows` | NDJSON，base64 ANSI 帧（首帧 `full:true`）；输入 `terminal.input / resize / scroll / release` |

补充一个决定路线的事实：herdr 的稳定 bincode 契约（generation 1）里，客户端能发的 JSON API 只白名单 37 个方法（pane / tab / workspace / layout / worktree 的控制类），**不含** `session.snapshot`、`events.subscribe`、`agent.*`、`pane.read`，且同一连接只允许 1 个在途请求。拓扑与 agent 状态只能靠它推送的 `shell.snapshot.v1`，读屏幕与事件订阅仍要走 `herdr.sock`。也就是说无论走哪条渲染路径，`herdr.sock` JSON API 都是必需的，这进一步支持"先只做 JSON 路径"。

两个决定产品形态的事实：
- **observe 是裁剪窗口、不重排**（实测 60×12 观察 295×79 的 pane 得到左下角裁剪）；**control 会把 PTY 尺寸锁定为控制者视口并重排**，桌面 TUI 里该 pane 会跟着变（与 tmux 多客户端语义一致），但不阻断 TUI 打字。→ 手机端必须"接管"才能看到适配宽度的内容，离开要 release。
- Go 的 `x/crypto/ssh` 支持 `direct-streamlocal@openssh.com`，可直接把远端 `herdr.sock` 拉成本地 `net.Conn`，不需要 socat，也**不需要在 Go 里实现 bincode**。

### 1.3 tailcat 作为库
`tailcat.Server{Key, AllowedClients, OnTCP}` 让被控端把任意 TCP 端口交给我们自己的处理器；`tailcat.NewClient(addr).DialTCPPort()` 在服务端拿到 `net.Conn`。一条 WireGuard 隧道可承载多条 TCP 流。地址由服务端公钥 + DERP region 派生，固定 key 即固定地址；`AddAllowedClient` 支持运行时加白名单。内置 SSH 服务只支持 `session` channel（不够用），因此 agent 需要内嵌我们自己的最小 SSH 服务。tailcat 要求 Go 1.27（可通过 Go toolchain 自动选择所需版本）。库明确"无 API 稳定性承诺"，需锁版本。 补充：tailcat 与其依赖的 tailscale.com 伪版本必须成对升级（tailcat 用了专为它加的上游钩子）；浏览器 WASM 直连（web/）只走 DERP、不打洞、27 MB、还要自写 SSH 客户端，**不作为主路径**，全部经 Go 服务端中转；agent 二进制用官方 build-tags 构建约 16 MB，`CGO_ENABLED=0` 可交叉编译。

### 1.4 参考项目的可借鉴点
- **orca**：xterm.js 6.1 beta + webgl/fit/unicode11/search/web-links/ligatures/serialize 全套 addon；WebGL 上下文丢失计数后自动降级；主进程用 `@xterm/headless` 做终端状态权威（我们不需要，herdr 服务端已是权威）；移动端"直接输入模式"为默认（隐藏捕获输入框直接转发按键），并保留"缓冲命令框"作为可选。
- **paseo**：配对二维码 = `https://app/#offer=base64url(JSON{v, serverId, pubKey, relay})`，fragment 不进服务器日志，同一 URL 既是二维码也是可粘贴链接；E2EE 用 tweetnacl box（X25519）握手；Docker 单镜像 + `/home` 卷 + 密码环境变量。它**缺**一次性 token、设备列表与单设备撤销，我们要补上。
- **herdr 自身**：窄屏模式（≤64 列）header + 单 pane + 四段式导航列表；Catppuccin Mocha 调色板的语义角色（red=blocked、yellow=working、blue/teal=done、green=idle、mauve=branch、peach=warning）；状态图标 dots（● ○ ·）与 symbols（× ◐ ✓ ○ ·）两套。

## 2. 产品方案

### 2.1 用户旅程
1. **注册 / 登录**：邮箱 + 密码（argon2id）；邀请制（首个注册者为管理员，之后管理员发邀请码）；可选 TOTP。
2. **主机列表**：卡片显示名称、传输方式（SSH / tailcat）、在线状态、herdr 版本、需要关注的 agent 数（blocked 红点）。
3. **添加主机向导**
   - SSH：填 host / port / user；认证选"使用 herdrx 为我生成的密钥（推荐，复制公钥到 authorized_keys）/ 上传私钥 / 密码"；首次连接展示主机指纹确认（TOFU）；检测远端 herdr 版本，缺失时给安装指引。
   - tailcat：在被控机执行 `herdrx-agent pair` 出二维码 → 手机扫码或电脑粘贴链接 → 服务端用该用户的 tailcat 身份拨号、出示一次性 token → agent 记录白名单 → 完成。
4. **工作台**（桌面）：左 sidebar（workspaces + agents）、顶部 tab 栏、中间 BSP 分屏终端、右下状态条（连接状态 / prefix 态提示 / 直连或 DERP）。
5. **工作台**（手机）：默认进入 agents 列表；点一个 agent 进入全屏单 pane（自动接管、按手机宽度重排）；底部虚拟键条；左滑右滑切 pane / tab；顶部下拉切 workspace。
6. **通知**：agent 变 blocked / done 时浏览器通知 + 可选声音（PWA 安装后走 Web Push）。
7. **设置**：终端主题（Cobalt2 默认）、字体字号、prefix 键、键位覆盖、手机键条布局、通知偏好、设备与会话管理。

### 2.2 页面清单
`/login` `/register` `/invite/:code` · `/hosts` `/hosts/new` `/hosts/:id/edit` · `/h/:id`（工作台，`?ws=&tab=&pane=` 可深链） · `/settings/{profile,terminal,keys,devices,notifications}` · `/admin/{users,invites}`

### 2.3 工作台交互（桌面）
- **sidebar**：workspace 行 = 编号 + 名称 + branch(mauve) + git ahead/behind + 上卷状态图标；worktree 子项 `├─ └─` 缩进；agents 面板 = 状态图标 + agent 名 + workspace（多 tab 时加 tab）+ 当前 state label；按 herdr 的 attention 优先级排序（blocked > done > working > idle）。`prefix+b` 折叠。
- **tab 栏**：编号 + 名称 + 上卷状态；`+` 新建；右键重命名/关闭；zoom 时显示"zoomed"标记。
- **pane**：标题条（label / agent / cwd 缩略）、聚焦边框用 accent；可拖 gutter → `layout.set_split_ratio`；右键菜单 split right/down、zoom、rename、close、copy、paste、"在 $EDITOR 打开 scrollback"的 Web 替代（下载 / 新窗口）。
- **prefix 状态机**：`ctrl+b`（可配）进入 prefix 态，状态条与光标处出现提示；第二键映射到 herdr API（映射表见附录 B）；`ctrl+b ctrl+b` 透传字面 `\x02`；`esc` 取消。`prefix+r` 进入 resize 模式（hjkl / 方向键连续调整，esc 退出）。`prefix+?` 键位面板，`prefix+g` 命令面板（模糊搜索 workspace / tab / pane / agent / 动作）。
- **浏览器冲突**：`ctrl+w / ctrl+t / ctrl+n / ctrl+shift+n` 无法被页面拦截，只在 PWA 独立窗口中部分可用；文档明示，并提供 `ctrl+a` 等备选 prefix 与命令面板兜底。
- **终端**：真彩、CJK 宽字符（unicode11）、OSC 8 链接可点、鼠标上报透传、滚轮 scrollback（通过 `terminal.scroll` 走服务端 scrollback，而不是 xterm 本地缓冲，保持与 TUI 一致）、拖选即复制、粘贴多行确认、搜索（ctrl+shift+f）。
- **状态机与浮层对齐 TUI**：客户端五种模式 Terminal / Prefix / Navigate / Resize / Copy；浮层集合 Help（prefix+?）、Navigator（prefix+g）、Rename、ConfirmClose、ContextMenu、GlobalMenu、Settings、WorktreeCreate / Open / Remove、ReleaseNotes、Onboarding。Web 端按同一清单实现，命名一致便于对照。直接组合键（非 prefix，如 ctrl+alt+…）采用 orca 的 "herdr-first / terminal-first + 白名单透传" 策略，避免吞掉应用需要的按键。新 pane 的 cwd 策略沿用 herdr 的 `terminal.new_cwd = follow|home|current|<path>`（默认跟随来源 pane）；CLI 创建默认不抢焦点，UI 操作聚焦新对象。
- **所有权策略**：进入 pane 默认 observe（视口按 pane 真实尺寸打开，不裁剪）；首次按键或点击"接管"升级为 control（默认 takeover 可在设置里关）；失焦 30s / 切走 / 关闭标签自动 release。状态条显示"观察 / 已接管"。

### 2.4 手机端（照 herdr 窄屏蓝本）
- 信息架构：**agents**（两行：状态+名 / 明细）→ **spaces**（+ new、树）→ **tabs**（+ new、列表）→ **menu**（settings / keybinds / reload config / detach）。
- 终端全屏，顶部一行 header（workspace 状态 + 名称 + tab 状态 + 右上切换按钮）。
- 虚拟键条：`Esc Tab Ctrl Alt ↑ ↓ ← → - / | ~ Ctrl+C ⌘B(prefix)`，Ctrl/Alt 粘滞一次；`⌘B` 点击进入 prefix 态并展开动作面板（split / zoom / close / new tab / new ws / rename / detach）。
- 手势：左右滑切 pane（同 tab）/ 长滑切 tab；下拉出 workspace 选择；双指缩放改字号（触发 resize → PTY 重排）；长按弹复制/粘贴/选择模式。
- 输入：默认"直接输入"（隐藏 textarea 捕获，中文 IME 组合完成后整体提交），可切"缓冲命令框"（适合手机打长命令）。
- 键盘弹出用 `visualViewport` 跟随缩小终端并 resize。
- PWA：manifest + service worker，可安装到桌面；Web Push 通知 blocked / done。

### 2.5 外观
- **UI 浅色**：白底 `#FFFFFF`、面板 `#F6F7F9`、边框 `#E5E7EB`、正文 `#111827`、次文 `#6B7280`；强调色取 Cobalt 蓝 `#1478DB`（与终端主题呼应）。状态色用 herdr Catppuccin Latte 的语义值：blocked `#D20F39`、working `#DF8E1D`、done `#1E66F5`、idle `#40A02B`、branch `#8839EF`、warning `#FE640B`，保证与 TUI 同一套语义。
- **终端默认 Cobalt2**（Wes Bos 官方 itermcolors 精确换算，见附录 C），另内置 Catppuccin Mocha / Dracula / One Dark / Tokyo Night / Solarized Dark / GitHub Dark；主题即 xterm `ITheme` JSON，用户可自定义并导入 itermcolors。 `minimumContrastRatio` 按背景亮度取 4.5（浅底）/ 3（深底）。
- 字体：JetBrains Mono / Fira Code（含 ligatures addon 开关），中文回退 Noto Sans Mono CJK。

### 2.6 功能清单
- **P0**：认证与邀请；SSH 主机（生成密钥 / 上传 / 密码）；工作台三层结构与状态语义；BSP 分屏 + 拖拽比例；xterm 终端（Cobalt2）；prefix 核心键位（附录 B 的 P0 列）；observe/control 策略；断线重连；Docker 部署。
- **P1**：tailcat agent + 二维码配对 + 设备列表/撤销；手机窄屏模式 + 键条 + 手势 + PWA；通知（浏览器 / Web Push）；命令面板与键位面板；右键菜单；主题切换与自定义；"TUI 直出模式"（在 xterm 里直接跑 `ssh host herdr`，作为 100% 原版兜底，成本极低）。
- **P2**：agent 快捷操作（prompt / wait / send_keys）；worktree UI；scrollback 导出；图片粘贴桥接（上传到远端临时文件再粘路径，对齐 `herdr --remote` 独有能力）；popup / 插件 pane 展示；多命名会话；TOTP / Passkey；审计日志；公共/自建 DERP 切换；kitty 键盘协议增强（CSI u）。

## 3. 技术架构

### 3.1 总体拓扑
```
┌──────────────────────────────┐        ┌──────────────────────────────────────────────┐
│  浏览器 (PC / 手机 PWA)        │        │  herdrx-server (Go, Docker, 部署在任意位置)     │
│  React + xterm.js            │◄─WSS──►│  REST(auth/hosts/settings) + WS(workbench)   │
│  浅色 UI + Cobalt2 终端        │        │  SQLite ─ 用户/主机/凭据(加密)/设置            │
└──────────────────────────────┘        │  HostConn: 统一成 *ssh.Client                 │
                                        │    ├─ SSHDirect ──► sshd:22 (公网/内网直连)     │
                                        │    └─ Tailcat   ──► tailcat.Client.DialTCPPort │
                                        └───────────┬───────────────────┬──────────────┘
                                                    │                   │ WireGuard P2P (DERP 兜底)
                                                    ▼                   ▼
                              ┌──────────────────────────┐   ┌──────────────────────────────┐
                              │ 主机 A (有 sshd)          │   │ 主机 B (NAT 后 / 无公网)        │
                              │ sshd ─► exec herdr ...   │   │ herdrx-agent (Go 单二进制)      │
                              │      ─► streamlocal ─►   │   │  ├─ tailcat.Server(固定 key)    │
                              │   ~/.config/herdr/       │   │  └─ 内嵌 SSH server            │
                              │     herdr.sock (JSON API)│   │     session exec + streamlocal  │
                              │   herdr server           │   │  ─► 同样接到本机 herdr           │
                              └──────────────────────────┘   └──────────────────────────────┘
```
核心思想：**Go 服务端永远以 SSH 协议访问主机**。直连主机用系统 sshd；tailcat 主机用 agent 内嵌的最小 SSH 服务（只允许 exec `herdr …` 白名单命令与转发 herdr socket）。两条路之上是同一个 `HostConn` 抽象，herdr 会话层不感知网络形态。

### 3.2 herdr 会话层（每主机每用户一份，多浏览器标签共享）
1. `sshClient.Dial("unix", "<herdr.sock>")` 两条：请求/响应（按 id 复用）+ `events.subscribe` 长连接。
2. 启动：先订阅并缓冲 → `session.snapshot` → 装载 → 按 revision 幂等回放缓冲事件（官方建议流程；实测订阅会回放历史，必须幂等）。
3. 终端流：每个 (pane, 浏览器客户端) 一个 SSH exec 通道跑 `herdr terminal session observe|control <pane> --cols N --rows M`；服务端解 base64 后以**二进制 WS 帧**下发；输入侧把按键编码成 `terminal.input` NDJSON 写 stdin。
4. 所有权：见 2.3；服务端记录每个终端当前 controller，多标签间协调（同一用户后接管者胜出）。
5. 健康：SSH keepalive 15s×3；tailcat 侧 `DiscoPing` 30s 并把"直连 / DERP"显示在状态条；事件流断开 → 重新 snapshot；终端流重开首帧 `full:true` → 前端 `term.reset()` 后写入。主机级重连采用 orca 的阶梯：`[1,2,5,5,10,10,10,30,30]s`，稳定 60s 后重置，短暂抖动不计失败，连续 9 次失败转为"需要手动重连"，UI 用 8 态连接状态机 + 覆盖层提示。
6. 版本检查：`session.snapshot.version / protocol`，要求主机 herdr ≥ 0.8.2；不满足给出升级提示。

### 3.3 服务端模块与选型（Go 1.26+，单二进制，前端资源 `embed`）
| 模块 | 职责 | 选型 |
|---|---|---|
| http | REST + 静态资源 + WS 升级 | `net/http` + `chi`；WS `github.com/coder/websocket` |
| auth | 注册 / 登录 / 邀请 / 会话 / TOTP | argon2id、HttpOnly+SameSite Cookie、CSRF token、登录限速 |
| store | 用户、主机、凭据、设置、设备、配对 | SQLite（`modernc.org/sqlite` 纯 Go）+ `golang-migrate` + `sqlc` |
| secrets | 凭据落库加密 | AES-256-GCM，主密钥 `HERDRX_MASTER_KEY`（缺省生成到数据卷，0600） |
| hostconn | 建立 `*ssh.Client` | `x/crypto/ssh`；known_hosts TOFU + 指纹入库；密码 / 私钥 / 服务端生成密钥 |
| tailcat | 每用户一把 client node key；拨号、ping、DERP map | `github.com/tailscale/tailcat`（锁 commit） |
| herdr | HerdrSession：API 复用、事件、快照缓存、终端流、所有权 | 自研 |
| ws | 浏览器协议 | 自研信封（3.4） |
| push | Web Push（VAPID） | P1 |
| obs | slog 结构化日志、`/healthz`、可选 `/metrics` | |

### 3.4 浏览器 ↔ 服务端 WS 协议（吸收 paseo 的成熟做法）
- **握手**：升级后 15s 内必须收到 `{"t":"hello","client_id","client_type":"browser|pwa","protocol":1,"capabilities":{...}}`，否则 4001 关闭；hello 前收到业务帧 4002；协议不兼容 4003；鉴权失败 4401。服务端回 `{"t":"server_info","server_id","version","features":{...},"user_id"}`。
- **可恢复会话**：服务端以 `(user_id, client_id)` 为键保留会话（断线后保留 60s），重连命中则挂回原会话，订阅与终端流槽位不丢；多标签共用 client_id 时可挂多个 socket。
- **文本帧 JSON**：请求 `{"t":"req","id","method","params"}` → `{"t":"res","id","result"}` 或 `{"t":"err","id","code","message"}`；事件 `{"t":"ev","event","data","seq"}`；连接态 `{"t":"conn","state":"connecting|ssh|herdr|ready|degraded","path":"direct|derp|ssh"}`。方法名与 herdr JSON API 同名直通（服务端只做鉴权与转发），herdrx 自有方法加 `x.` 前缀（`x.terminal.open`、`x.control.acquire/release`、`x.host.status`）。
- **二进制帧**：`[opcode u8][slot u8][payload]`，slot 由 `x.terminal.open` 响应分配（每连接最多 255 个终端流）；opcode 1=输出 2=输入 3=resize 4=scroll 5=release 6=打开确认。首字节判别二进制/JSON，同一条 WS。
- **同步模型**：服务端为 workspaces / tabs / panes / agents / layouts 各维护"每实体最新投影 + 全局单调 seq + tombstone"，进程启动生成 `generation`；客户端重连带 `{generation, after_seq}` 拉增量，generation 变或 cursor 过期则全量快照。herdr 事件流经服务端归一化后再打 seq，前端不直接消费 herdr 原始事件。
- **流控**：herdr 服务端对每个客户端 render 通道只有 1 个槽位，读慢只会漏中间帧然后收到全量，天然防积压；herdrx-server → 浏览器方向以 `term.write(data, cb)` 回调为背压，手机端按 orca 经验做约 48ms 的写合并；P2 再评估 orca serve 式 Ack 滑动窗口（5ms 合批 / 48KB 分块 / 512KB 窗口）。
- **心跳分层**：传输层 `ping/pong` 10s 一次、15s 超时、连续 2 次失败才重连（退避 1.5s×2ⁿ，上限 30s）；应用层 presence（前台 pane、可见性）只影响通知路由与 done→idle 的"已看过"判定，不影响数据正确性。
- **兼容纪律**：新字段只加 optional；新能力通过 `server_info.features.*` 一次性 gate；每个兼容 shim 带 `COMPAT(name): added vX, remove after <date>` 注释。

### 3.5 数据模型（SQLite）
`users`(id, email, password_hash, display_name, role, totp_secret?, disabled, created_at) · `sessions`(id, user_id, token_hash, ua, ip, expires_at, last_seen) · `invites`(code, created_by, expires_at, used_by) · `hosts`(id, user_id, name, transport, herdr_session, icon, last_seen, last_status) · `host_ssh`(host_id, hostname, port, username, auth_method, credential_id, host_key_fp) · `host_tailcat`(host_id, tc_addr, agent_node_pub, agent_ssh_pub, paired_at) · `credentials`(id, user_id, kind, ciphertext, nonce, label) · `user_tailcat_identity`(user_id, node_priv_ct, node_pub, ssh_priv_ct, ssh_pub) · `user_settings`(user_id, json) · `push_subscriptions` · `audit_log`(P2)

### 3.6 安全
- 密码 argon2id；会话 Cookie HttpOnly/Secure/SameSite=Lax；WS 复用 Cookie 鉴权 + Origin 校验；REST CSRF token；登录 / 配对接口限速。
- 凭据（SSH 密码、私钥、tailcat 私钥）AES-GCM 加密落库；主密钥不进库；轮换工具 P2。
- SSH：known_hosts TOFU，指纹变化阻断并要求用户确认；默认推荐"服务端为用户生成 ed25519 密钥"避免上传私钥。
- tailcat：WireGuard 身份白名单（agent 只接受已配对用户的 node key）+ 内嵌 SSH 再做一层公钥认证 + exec 命令白名单 + streamlocal 路径白名单；配对 token 一次性、10 分钟过期。
- 多租户隔离：所有主机 / 凭据按 user_id 归属；不做共享（v1）。
- 输入内容不落日志；终端流不持久化。

### 3.7 前端
| 层 | 选型 |
|---|---|
| 框架 | React 19 + TypeScript + Vite（vite-plugin-pwa） |
| 样式 / 组件 | Tailwind CSS v4 + shadcn/ui（Radix），浅色系统 UI |
| 状态 | zustand（工作台实时状态）+ TanStack Query（REST） |
| 路由 | TanStack Router |
| 终端 | `@xterm/xterm`（5.5 稳定版，6.x 稳定后升级）+ addon-webgl / fit / unicode11 / web-links / search / clipboard / image / ligatures。借鉴 orca：WebGL 三态设置（auto 探测 webgl2 与软渲染器名、Linux Wayland 关闭），addon 懒加载，上下文丢失 60s 内 3 次即停用；`term.write(data, cb)` 回调作背压并向 SSH 通道回传 credit；IME 组合期把隐藏 textarea 锚到光标 cell 让候选框跟随；前台为已知 agent TUI 时强制 bracketed paste；无修饰键点链接弹操作菜单而非直接打开 |
| 布局 | 自研 BSP 渲染（数据源 `layout_updated`）+ 可拖 gutter；拖动期间只本地 fit xterm，松手才回写 `layout.set_split_ratio` 并发 `terminal.resize`；ResizeObserver 等尺寸稳定（≤8 帧）再 fit；手机单 pane |
| 快捷键 | document 级 keydown 拦截 + xterm `attachCustomKeyEventHandler` 实现 prefix 状态机；映射表来自用户设置 |
| 国际化 | zh-CN 默认 + en |

### 3.8 herdrx-agent（被控端）
- 单静态二进制（Linux / macOS，amd64 / arm64），`herdrx-agent install` 生成 systemd / launchd 服务。
- **安装即白名单**：webapp 为每个用户生成一条安装命令，内含该用户的 tailcat 公钥与 SSH 公钥（`curl -fsSL https://<herdrx>/install.sh | sh -s -- --allow nodekey:… --ssh-key 'ssh-ed25519 …'`），agent 首次启动就处于白名单模式，**不存在 allow-all 窗口**。
- `pair`：读取或生成固定 tailcat key（固定 DERP region，沿用 tailcat 的 `PrivateKey` JSON 格式落盘 0600，便于用 tailcat CLI 排障），打印二维码 + 链接 `https://<herdrx>/#pair=base64url({v, tc, host, os, arch, agent_ver, tok, exp})`。二维码只负责把长地址与一次性 token 交给服务端；链接 fragment 不进服务器日志；同一内容可扫可粘。
- 配对：服务端用该用户 tailcat 身份拨 `tc`，通过内嵌 SSH 认证后执行 `herdrx-agent confirm-pair <tok>`（HMAC 挑战响应）→ agent 标记已配对、关闭配对窗口 → 服务端写入 `host_tailcat`。撤销 = 服务端删记录 + 在线时通过 SSH 吊销该用户公钥（立即生效）。
- `run`：`tailcat.Server{Key, AllowedClients, OnTCP}`；`OnTCP(22)` → 内嵌 SSH server（x/crypto/ssh 服务端）：公钥认证；`session` 只允许 exec 白名单（`herdr …`、`uname -sm`、`test -S …`）；`direct-streamlocal` 只允许 herdr 配置目录下的 socket；`OnTCP(2222)` 可选转发系统 sshd 作为兜底。
- DERP：**默认使用 herdrx 自带的 derper**（安装命令里已含 DERP 主机名，地址为嵌入 region 的长地址，客户端零查询），试用场景可切 `tailcat.dev` 公共中继；`herdrx-agent status` 显示直连 / DERP。
- 拓扑：MVP 用拓扑 A（agent = tailcat.Server，herdrx-server = Client，每 agent 一套引擎）；角色抽象在接口后，在线主机 >100 台时可切拓扑 B（herdrx-server 单 Server，agent 为 Client 反向拨入 + yamux）。
- **tailcat 库层约束（源码核实）**：
  - 地址即能力：拿到地址就能连（无白名单时），所以配对载荷里的 `tc` 地址必须配合一次性 token 与白名单使用；agent 启动即 `AddAllowedClient(零值 key)` 进入"先关门"模式，配对成功再逐个放行。
  - 持久化地址**必须固定 DERP region**（生成 key 时指定 region 或嵌入 Region），否则 agent 重启重选区域会让已发出的地址失效；`Server.TailcatAddr()` 返回嵌入 region 的长地址，直接用于二维码（客户端零查询、兼容自建 DERP）。
  - 白名单只增不减、无删除接口、已连上的客户端不会被驱逐 → **撤销靠内嵌 SSH 层的公钥吊销**（立即生效）+ agent 下次重启时重建白名单。
  - 任一侧进程重启后对端的 `Client` 对象作废且 `Ping()` 会假成功 → 服务端每 agent 一个监督循环：`DiscoPing` 30s 探活，连续 2 次失败即 `Close()` 重建 Client，指数退避；业务 Dial 超时 15s。
  - 同一 client key 不能被两个进程同时使用 → herdrx-server 单实例部署；将来多副本需为每副本派发独立 key。
  - 反向连接不支持（agent 不能主动拨服务端）→ 所有交互由服务端发起，agent 侧无需能访问 herdrx 公网地址，这正好符合"部署在任何地方"的目标。

### 3.9 部署
- 多阶段构建：node 构建前端 → go 编译并 embed → distroless 运行；镜像发布 `ghcr.io/<you>/herdrx`。
- `docker compose`：`herdrx`（:8080，卷 `/data`：sqlite、主密钥、tailcat 身份）+ `derper`（默认启用，443/tcp、80/tcp、3478/udp，与 herdrx 的 go.mod pin 同一 tailscale.com 版本）+ 可选 `caddy`（自动 HTTPS；WSS 与 PWA 都要求 HTTPS）。
- 环境变量：`HERDRX_ADDR` `HERDRX_DATA_DIR` `HERDRX_MASTER_KEY` `HERDRX_PUBLIC_URL` `HERDRX_REGISTRATION=invite|open|closed` `HERDRX_DERP_MAP_URL` `HERDRX_VAPID_*`。
- 仓库结构（monorepo）：`/server`（Go）`/agent`（Go，共享 `/internal`）`/web`（React）`/deploy`（Dockerfile、compose、Caddyfile）`/docs`。

## 4. 关键决策（ADR 摘要）
| 决策 | 选择 | 备选与放弃原因 |
|---|---|---|
| 接入 herdr 的协议 | JSON API + `terminal session control`（NDJSON） | Go 实现 bincode generation-1 契约：能拿到服务端渲染的 cell 网格与 popup，但要冻结枚举顺序、维护 bincode 2 varint 编解码，且平铺布局受服务端约束不利于手机；留作 P3 可选 |
| 终端渲染 | 每 pane 一个 xterm.js，喂服务端 ANSI 帧 | 自研 cell 网格 canvas：需自己处理字体 / IME / 选区 / 链接，收益低 |
| 布局 | 前端按 `layout_updated` 的 BSP 自绘，比例改动回写 `layout.set_split_ratio` | 直接复用 TUI 绝对矩形：把 24 列 sidebar 等 TUI 尺寸带进来，不适合响应式 |
| 主机传输 | SSH 统一（sshd 或 agent 内嵌 SSH） | agent 自定义 yamux 协议：少一层 SSH 但两条路径要写两套，且失去 SSH 成熟的认证 / 通道模型 |
| P2P | tailcat 库嵌入 agent 与服务端 | 让用户装 tailscale：需要账号与控制面，违背"任何网络都能用、部署在任何地方"；tailcat CLI 直接 `serve 22`：需主机有 sshd，macOS 笔记本常没有 |
| 数据库 | SQLite 单文件 | Postgres：数据量极小，多一个容器不值 |
| 所有权 | observe 默认 + 按需 control + 自动 release | 永远 control：会持续锁定桌面 TUI 的 pane 尺寸 |
| 手机信息架构 | 照 herdr 窄屏模式 | 自创：偏离"原汁原味" |

## 5. 风险与应对
| 风险 | 影响 | 应对 |
|---|---|---|
| herdr 私有 CLI / JSON API 变更 | 终端流或事件格式变化 | 只依赖文档化路径；启动检查 `snapshot.protocol`；CI 用 herdr 稳定版做端到端回归 |
| tailcat 无 API 稳定性承诺 | 升级破坏 | 锁 commit；agent 与服务端同版本发布；tailcat 逻辑封装在单一包 |
| 公共 DERP 限速 / 不可用 | 打洞失败时无兜底 | compose 一键自建 derper；状态条显示路径；提供 SSH 直连备选 |
| 手机浏览器输入（IME、组合键、剪贴板权限） | 体验差 | 隐藏 textarea 直输 + 缓冲框双模式（orca 经验）；键条覆盖缺失键 |
| control 锁尺寸打扰桌面使用 | 用户困惑 | 状态条明示 + 自动 release + 设置里可改"只观察" |
| 每 pane 一个 SSH exec 进程 | 主机进程数 | 只为可见 pane 开流；隐藏 pane 关流仅保留元数据；SSH 单连接多 channel |
| `terminal session control` 不透传 pane 协商的 kitty 键盘协议标志 | 请求 CSI u 的 TUI 收到的是传统编码的组合键 | P0 用传统编码（与多数终端一致）；P2 特殊键改走 `pane.send_keys`（服务端按协商编码）或 generation-1 bincode 路径 |
| Go 1.27 要求 | 构建环境 | `GOTOOLCHAIN=auto`，CI 固定 toolchain |
| tailcat 白名单无法在线撤销 | 被撤销用户在 agent 重启前仍可建立 WireGuard 会话 | 内嵌 SSH 公钥吊销立即拒绝；撤销后提示用户重启 agent；配对端口只在配对窗口内开放 |
| agent 重启后服务端 Client 假成功 | 连接看似正常实则不通 | DiscoPing 监督循环 + 失败重建 Client（见 3.8） |
| xterm.js 6 仍 beta | 兼容性 | 先用 5.5 稳定版，addon 版本锁定 |

## 6. 分阶段计划与验收
| 阶段 | 内容 | 验收 |
|---|---|---|
| **P0 技术验证（3–5 天）** | Go：SSH → streamlocal → snapshot/events；exec `terminal session control`；WS → 单页 xterm。tailcat：agent 内嵌 SSH + 服务端拨号，跑通同一条链路 | 在两台真实主机（一台 sshd 直连、一台 NAT 后 tailcat）上，浏览器里能看到并操作一个 pane，断网重连不丢内容 |
| **P1 MVP（2–3 周）** | 认证 / 邀请、SSH 主机 CRUD、桌面工作台（sidebar / tab / 分屏 / prefix 核心键 / Cobalt2）、observe/control、Docker | 日常开发可以完全在浏览器里进行；`docker compose up` 一条命令部署 |
| **P2 tailcat + 手机（2–3 周）** | agent 发布与配对、设备管理、手机窄屏模式 + 键条 + 手势、PWA、通知、主题切换、TUI 直出模式 | 手机扫码 1 分钟内接入 NAT 后的机器；blocked 时手机收到推送并能在 30 秒内回复 agent |
| **P3 增强（持续）** | agent 快捷操作、worktree UI、图片粘贴桥接、TOTP/Passkey、审计、自建 DERP 开关、popup/插件 pane | 按需 |

## 7. 需要拍板的问题
1. **注册模式**：邀请制（首个用户为管理员）还是开放注册？→ 推荐邀请制，环境变量可切。
2. **SSH 私钥策略**：默认"服务端为每个用户生成 ed25519 密钥、用户把公钥放到主机"，同时允许上传私钥 / 用密码？→ 推荐是。
3. **tailcat 配对方向**：安装命令预置白名单 + 被控端出二维码 + 服务端主动拨号（被控端不需要能访问 herdrx）作为唯一方式，还是再加"agent 主动注册到服务端 URL + 配对码"？→ 推荐先只做前者（tailcat 也不支持 agent 反向发起连接）。
4. **DERP**：compose 默认自建 derper（推荐，公共中继限速且可撤销），公共 `tailcat.dev` 仅作试用开关；接受吗？
5. **手机首页**：agents 列表（herdr 蓝本）还是直接进最近的终端？→ 推荐 agents 列表。
6. **桌面布局忠实度**：严格镜像 herdr 的 BSP 平铺（推荐），还是允许 Web 端自由拖成任意布局？
7. **TUI 直出模式**是否要做（xterm 里跑原版 herdr TUI，作为兜底与对照）？→ 推荐 P2 做，成本半天。
8. **主题清单与浅色 UI 语义色**：Cobalt2 默认 + 上述 6 个内置；UI 状态色采用 Catppuccin Latte 值。OK？
9. **仓库与命名**：monorepo `/server /agent /web /deploy /docs`，Go module 路径（例如 `github.com/riba2534/herdrx`）？
10. **共享主机**：v1 是否需要"一台主机被多个用户添加/共享"？→ 推荐不做。

## 附录 A：herdr 协议速查（实测于 0.8.2）
- 请求：`{"id":"r1","method":"pane.split","params":{"pane_id":"w1:p1","direction":"down","ratio":0.5}}`
- 订阅：`{"id":"e1","method":"events.subscribe","params":{"subscriptions":[{"type":"pane.created"},{"type":"layout.updated"},{"type":"pane.agent_status_changed","pane_id":"w1:p1"}]}}` → `{"result":{"type":"subscription_started"}}` → 每行 `{"event":"pane_created","data":{...}}`
- 事件类型（27）：workspace.{created,updated,metadata_updated,renamed,moved,reordered,closed,focused}、worktree.{created,opened,removed}、tab.{created,closed,focused,renamed,moved}、pane.{created,closed,updated,focused,moved,exited,agent_detected,output_matched*,agent_status_changed*,scroll_changed*}、layout.updated（* 需 pane_id）
- 终端帧：`{"type":"terminal.frame","seq":1,"encoding":"ansi","width":100,"height":30,"full":true,"bytes":"<b64>"}`；结束 `{"type":"terminal.closed","reason":...}`
- 终端命令：`{"type":"terminal.input","text":"..."}` | `{"type":"terminal.input","bytes":"<b64>"}` | `{"type":"terminal.resize","cols":80,"rows":24}` | `{"type":"terminal.scroll","direction":"up","lines":3,"source":"wheel"}` | `{"type":"terminal.release"}`
- 布局：`layout_updated.data.layout = {area, focused_pane_id, panes:[{pane_id, focused, rect}], splits:[{id, direction:"right|down", ratio, rect}], zoomed}`；BSP 树用 `layout.export`
- agent_status：`idle | working | blocked | done | unknown`
- socket 路径：`~/.config/herdr/herdr.sock`；命名会话 `~/.config/herdr/sessions/<name>/herdr.sock`

## 附录 B：默认键位 → herdr API 映射
| 动作 | 默认键 | API | 优先级 |
|---|---|---|---|
| 新 tab / 上下切换 / 跳 1-9 | prefix+c / prefix+n prefix+p / prefix+1..9 | tab.create / tab.focus | P0 |
| 分屏右 / 下 | prefix+v / prefix+minus | pane.split | P0 |
| 焦点 hjkl / 轮换 | prefix+h j k l / prefix+tab, shift+tab | pane.focus_direction / pane.focus | P0 |
| 缩放 / 关闭 pane | prefix+z / prefix+x | pane.zoom / pane.close | P0 |
| 交换 pane | prefix+shift+h j k l | pane.swap | P0 |
| resize 模式 | prefix+r → hjkl | pane.resize | P0 |
| workspace 导航 / 新建 / 重命名 / 关闭 / 跳 1-9 | prefix+w / shift+n / shift+w / shift+d / shift+1..9 | workspace.* | P0 |
| tab 重命名 / 关闭 | prefix+shift+t / prefix+shift+x | tab.rename / tab.close | P0 |
| pane 重命名 | prefix+shift+p | pane.rename | P1 |
| 折叠 sidebar / 键位帮助 / goto | prefix+b / prefix+? / prefix+g | 本地 UI | P1 |
| 复制模式 | prefix+[ | 本地：搜索 + 键盘选区 | P1 |
| 通知目标 | prefix+o | 本地：跳到最近 blocked/done | P1 |
| 新 worktree | prefix+shift+g | worktree.create | P2 |
| 编辑 scrollback | prefix+e | 本地：下载 / 新窗口 | P2 |
| 重载配置 / 分离 | prefix+shift+r / prefix+q | server.reload_config / 断开 | P1 |
| 字面 ctrl+b | prefix+ctrl+b | 透传 0x02 | P0 |

## 附录 C：Cobalt2 终端主题（Wes Bos 官方 itermcolors 精确换算）
background `#173448` · foreground `#FFFFFF` · cursor `#F4D300` · cursorAccent `#FEFFF4` · selection `#1F4562`
black `#000000` red `#FF2600` green `#3DDF2B` yellow `#F4D300` blue `#1478DB` magenta `#FF2C70` cyan `#00C5C7` white `#C7C7C7`
brightBlack `#686868` brightRed `#F92A1C` brightGreen `#43D426` brightYellow `#F1D000` brightBlue `#6871FF` brightMagenta `#FF77FF` brightCyan `#79E8FB` brightWhite `#FFFFFF`

## 附录 D：herdr 调色板语义（Catppuccin Mocha 默认，Latte 为浅色）
accent 蓝 · red=blocked · yellow=working · blue/teal=done(未读) · green=idle · mauve=branch · peach=warning · panel_bg / active_row_bg / surface0-1 / overlay0-1 / text / subtext0 直接映射为 CSS 变量。
