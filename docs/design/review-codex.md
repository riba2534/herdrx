# herdrx v0.3 独立复核意见（Codex）

> 日期：2026-09-03
>
> 状态：评审稿，不代表用户已经拍板
>
> 复核范围：`herdrx-plan-v0.3.md`、`research/` 六份技术材料。未修改 herdr、tailcat、orca、paseo 参考仓库，也未开始写产品代码。

## 1. 总体判断

v0.3 的主方向是对的，以下选择应保留：

- 用 herdr 官方 JSON API 获取结构与状态，用 `terminal session observe/control` 获取终端，而不是在第一阶段实现 bincode。
- 浏览器只连接 herdrx-server，由 Go 服务端负责 SSH/tailcat；不让浏览器加载 tailcat WASM。
- 两种主机接入最终都收敛到“可执行受限命令 + 可访问 herdr Unix socket”的能力边界。
- Web 端忠实复刻 workspace/tab/pane/agent 与 BSP 拓扑，但自行做响应式渲染。
- xterm.js、Cobalt2、herdr 的 prefix/窄屏交互模型都与产品定位相符。

不过，v0.3 还没有达到“方案彻底没问题，可以开始完整实现”的程度。现在适合做的是一个有明确假设清单和退出标准的技术验证；在进入 MVP 之前，至少要补齐三类契约：

1. **终端正确性契约**：缺帧、背压、重连、全量帧、scrollback 分别如何处理。
2. **控制权契约**：浏览器、手机、多个标签页和原生 herdr TUI 同时存在时，谁能改变 PTY 尺寸、谁能输入、何时释放。
3. **信任与容量契约**：服务端能够看到所有凭据和终端明文；v1 实际是单实例多用户系统，拓扑 A 的资源成本与后台监控策略必须有上限。

我的结论是：**核心架构可继续，但应先将本文的阻塞项和 10 个待拍板问题合并成 v0.4，再进入正式产品开发。P0 技术验证可以在拍板后开始，且应被明确视为 spike，而不是 MVP 第一批生产代码。**

## 2. 必须在 P0 或 P0 之前澄清的问题

### R1（阻塞）：P0/P1/P2 的定义互相冲突

**风险/理由：** 方案 2.6 把认证、邀请、SSH CRUD、完整桌面工作台和 Docker 都列进 P0；第 6 节的 P0 却是 3–5 天技术验证。tailcat 又同时出现在 P0 验证和 P2 产品功能中。若不先统一，估时、验收和代码质量要求都会失真。

**建议：** 把阶段改成：

- **S0/Spike（3–5 天）**：只验证协议和网络假设，允许丢弃实现。
- **M1/桌面 MVP**：认证、SSH 主机、桌面工作台、生产化协议与部署。
- **M2/tailcat + 手机**：agent 发布、正式配对、手机、PWA、通知。
- **M3/增强**：worktree、图片、审计增强、Passkey 等。

技术验证里可以包含 tailcat，但不等于 tailcat 产品功能已完成。

### R2（阻塞）：“断网重连不丢内容”没有可执行定义

**风险/理由：** 至少有四种不同断线：浏览器↔服务端 WS 断开、服务端↔主机 SSH channel 断开、tailcat Client 失效、远端 herdr daemon 重启。重新打开终端后收到 `full:true` 可以让当前屏幕收敛，但不能自然保证断线期间每一段输出和完整 scrollback 都能恢复。

**建议：** 把承诺拆成四级，并为每种故障单独验收：

1. pane 进程不因浏览器/SSH 断线退出；
2. 重连后当前可见屏幕与 herdr 权威状态一致；
3. 浏览器短断线期间、服务端仍连着主机时，输出可连续恢复；
4. 主机链路也断开时，只承诺当前屏幕收敛，历史输出是否完整取决于 herdr 的可读缓冲。

P0 不应笼统写“不丢内容”，而应明确至少保证 1、2，并实测 3；若无法保证 4，应在产品文案中如实说明。

### R3（阻塞）：两字节终端帧无法承载恢复所需元数据

**风险/理由：** v0.3 的 `[opcode u8][slot u8][payload]` 没有传递 herdr 原帧的 `seq/full/width/height`，客户端无法判断是否缺帧、何时 `term.reset()`、slot 是否属于重连前的旧流。`slot u8` 也把一个连接硬限制在 255 个流，却没有复用代际。

**建议：** 从第一版就定义带版本的固定头，例如 Orca 的思路：`kind/version/opcode/flags/stream_id(u32)/seq(u64)`，并为每个流增加 `stream_epoch` 或在 open 响应里返回不可复用的代际。至少定义：

- snapshot/full frame 开始、数据、结束；
- 增量 output；
- resize/metadata；
- ack；
- gap/error/closed；
- 客户端主动请求全量快照。

同时做 Go↔TS golden vectors，协议字段只增不删。

### R4（阻塞）：当前方案并没有真正的浏览器背压

**风险/理由：** `term.write(data, callback)` 只在浏览器里可见；如果浏览器不把处理完成的信息回传，Go 服务端只能知道 WebSocket 写入了网络缓冲，不能知道 xterm 已解析。v0.3 一边声称用回调背压并回传 credit，一边又把 Ack 窗口推迟到 P2，二者不成立。

**建议：** 二选一，但必须在桌面 MVP 前完成：

- 实现显式 Ack/credit 滑动窗口；或
- 明确定义有界丢帧策略：每流限制排队字节，超限后丢弃增量、标记 dirty、重新打开/请求 full frame。

无论哪种，都要限制单帧、单流、单连接和单用户的缓冲上限；慢手机不能把 server 内存无限拖高。

### R5（阻塞）：snapshot + 历史事件回放算法还不够严谨

**风险/理由：** herdr 订阅会回放旧事件。服务端新增 `generation + seq + tombstone` 只能保证 herdrx 自己的投影有序，不能自动修正一个已经被旧事件回退的错误投影。尤其要验证删除事件、跨 workspace 移动、layout 全量事件，以及必须按 pane_id 订阅的 agent/scroll 事件；新 pane 出现后如何无缝补订阅也没有写清。

**建议：** P0 先收集真实事件 trace，给 reducer 做确定性回放测试。只有携带可比较实体 revision 的事件才作为状态变更；无法证明顺序的事件先作为“失效通知”，触发节流后的 fresh snapshot。还要专门验证：

- subscribe→buffer→snapshot 的精确 barrier；
- 新 pane 的动态订阅是否有空窗；
- move/close 后旧 ID 和 tombstone 如何处理；
- 事件流断开期间发生变化后能否由新 snapshot 完全收敛。

### R6（阻塞）：`client_id` 同时承担设备、标签页和恢复身份，语义冲突

**风险/理由：** 方案允许多个标签页共用 `client_id` 并把多个 socket 挂到同一 session，但终端 slot、presence、前台 pane、请求 ID 和控制权实际都是连接或标签页级状态。两个标签页共用一个恢复键会发生 slot 冲突、错误恢复或互相覆盖可见性。

**建议：** 分成四个概念：

- `device_id`：localStorage 持久化，只用于设备与偏好；
- `browser_instance_id`：sessionStorage 生成，每个标签页唯一；
- `connection_id`：每次 WS 由服务端生成；
- `resume_id`：短期恢复同一个 browser instance 的订阅。

可共享的是主机级 `HerdrSession` 和快照缓存，不是标签页级 slot/presence。一个恢复会话默认只允许一个活跃 socket，新连接替换旧连接时要有明确 close code。

### R7（阻塞）：控制权必须是服务端租约，不能依赖 blur/失焦

**风险/理由：** 手机进入后台、浏览器崩溃或网络半开时，`blur` 和 release 都可能永远不送达。首次按键自动升级 control 还可能在 SSH exec 切换期间丢掉第一键。若使用 `--takeover`，还可能突然抢走原生 herdr/TUI 的 controller 并改变其 PTY 尺寸。

**建议：** 建立显式控制租约状态机：`observing → acquiring → controlling → releasing/expired`。租约由服务端心跳续期，超时自动 release；首键先在浏览器侧有界缓存，收到 acquire 成功与 full frame 后再发送。发现已有外部 controller 时默认提示确认，不应把“第一次打字”视作无提示 takeover。手机尺寸变化要 debounce，并区分字号缩放与 PTY resize。

### R8（阻塞）：Web 本地焦点、herdr 全局焦点和 Done/seen 的关系未经验证

**风险/理由：** 原生 client-owned shell 的视图位置是 per-client；纯 JSON 的 `workspace.focus/tab.focus/pane.focus` 是否同样隔离尚未被材料证明。如果 Web 为了切页面就调用这些 API，可能改变原生 TUI 的焦点或把其他客户端的 Done 标成 seen。反过来，完全不调用又可能让 herdr 永远认为 Web 没看过。

**建议：** P0 必须做“双客户端同时连接”实验，验证 JSON focus、agent focus、terminal control 对原生 TUI 和 seen 的影响。在结论明确前，Web 的 workspace/tab/pane 选择应保持本地状态，所有变更 API 都带显式目标，不依赖 herdr 的“当前焦点”。Done 的权威来源和“用户看过”的写回方式要单独写成状态机。

### R9（高）：scrollback 有两个潜在权威，搜索和滚轮行为会冲突

**风险/理由：** 方案要求滚轮走 herdr `terminal.scroll`，但 xterm 自己也维护 scrollback；服务端送来的又是渲染 ANSI 帧，而非原始 PTY 字节。若两边同时积累，可能出现重复历史、搜索结果错误、滚轮被 xterm 吃掉，alt-screen 和开启鼠标上报的 TUI 更复杂。

**建议：** 先选一个权威模型：

- 若 herdr scrollback 权威，则 xterm 只保留很小本地缓冲，拦截滚轮/页键，搜索与导出改走 `pane.read`/服务端能力；
- 若 xterm 本地 scrollback 权威，则必须证明 full/incremental ANSI 帧能够无重复地重建历史，并定义断线缺口恢复。

P0 要覆盖普通 shell、全屏 TUI、mouse reporting、alt-screen、中文宽字符和快速大输出。

### R10（高）：远端 Unix socket 定位和 streamlocal 能力被过度假定

**风险/理由：** `x/crypto/ssh.Client.Dial("unix", "~/.config/...")` 不经过远端 shell，不能把 `~`、`$HOME` 或 `$XDG_CONFIG_HOME` 当成必然可展开；命名 session 的路径也不同。部分 sshd 还会禁用 streamlocal forwarding。远端 herdr daemon 未启动时 socket 也不存在。

**建议：** P0 先定义一个安全的 socket locator：通过受控 exec 得到远端绝对 config path、session 名和 daemon 状态，再发 streamlocal 请求。验证默认 session、命名 session、非默认 XDG 路径、daemon 未运行和 `AllowStreamLocalForwarding=no`。对后者至少给出精确诊断；若产品必须兼容，应准备一个最小 bridge/agent 兜底，而不是依赖 socat。

### R11（阻塞）：不能把 herdr JSON API 或 `herdr …` 命令按前缀直通

**风险/理由：** v0.3 写“方法名同名直通，服务端只做鉴权与转发”，agent 又写“允许 exec `herdr …` 白名单”。如果白名单只是字符串前缀，新版 herdr 新增危险方法、`server.stop`、插件/集成安装或命令参数注入都可能被意外暴露。

**建议：** 服务端维护显式、版本化的 method allowlist 与参数 schema，按 read/control/destructive/admin 分权限；不认识的方法默认拒绝。agent 不把命令交给 shell，也不接受任意 `herdr …` 字符串，而是严格解析固定子命令和参数后用 argv 执行；pane/session ID、尺寸、路径和输入长度都要校验。streamlocal 只允许解析后的精确 socket 路径。

## 3. 架构、安全与运维遗漏

### R12（阻塞）：产品信任模型没有写出来

**风险/理由：** herdrx-server 必须解密 SSH/tailcat 私钥并看到终端明文，实例管理员也技术上能够访问所有用户主机。这个架构不是端到端加密，也无法保护用户免受恶意服务端运营者。开放注册会把一个高价值凭据库暴露给不受信任租户。

**建议：** 在定位章节明确写：v1 信任 herdrx 实例及其管理员；面向个人、家庭或互相信任的小团队自托管。若未来要做不信任运营方的 SaaS，需要完全不同的本地代理/E2EE 授权架构。注册模式、审计、备份和密钥策略都应以这个威胁模型为前提。

### R13（高）：首个注册者自动成为管理员存在抢注

**风险/理由：** 新实例若先暴露到公网，攻击者可能比部署者先注册并成为管理员。邀请制并不能解决“第一个邀请者从哪里来”。

**建议：** 首启使用一次性 bootstrap token、CLI 初始化或环境变量预置管理员；初始化完成后立即销毁 token。默认 `closed` 或只监听 loopback，管理员创建完成后再切 `invite`。邀请 code 应只存 hash、单次消费且事务内校验过期与 used 状态。

### R14（高）：服务端生成的 SSH key 应按主机隔离，而不是按用户复用

**风险/理由：** 每用户一把 key 会让任意一台 agent、数据库泄漏或轮换操作影响该用户的所有主机；删除单台主机也无法独立撤销。把生成的公钥直接加入普通 `authorized_keys` 还等于给 herdrx 完整 shell 权限，而非仅 herdr 权限。

**建议：** 默认每个 host/pairing 生成独立 ed25519 key；上传私钥和保存密码保留为明确的高级选项，密码默认不记住。UI 要说明普通 SSH key 的权限范围。对安全敏感用户优先推荐 herdrx-agent 的受限 SSH；若 direct sshd 要做到最小权限，需要另行设计可验证的 forced-command/helper 方案，不能只靠文案。

### R15（高）：tailcat 配对的 agent 身份校验与撤销流程不闭合

**风险/理由：** QR 里有 tailcat 地址和 token，却没有明确包含/固定内层 SSH host key；`HMAC 挑战响应`也没有定义共享 HMAC 密钥。删除服务端记录后再去远端撤销会失去连接，agent 离线时更不能声称“立即撤销”。删除 authorized key 也不会自动终止已经认证的活跃 SSH 连接。

**建议：** QR 加入 agent SSH host public key/fingerprint，服务端在第一次 SSH 握手时精确验证；token 只证明本地用户看到了 QR，并做一次性原子消费。无需未定义的 HMAC，除非先定义密钥来源和 transcript。撤销顺序应是：agent 内存拒绝新认证并关闭该 key 的活跃连接 → 持久化撤销 → 服务端删/封存记录；离线时标记 `revocation_pending`，同时提供本机 `unpair` 命令与明确告警。

### R16（阻塞）：agent 的 OS 用户与 herdr socket 所属用户没有定义

**风险/理由：** herdr socket 位于运行 herdr 的用户配置目录。若安装脚本把 agent 作为 root system service 运行，它会看到 root 的 HOME，而不是开发者的 session；macOS launchd、Linux systemd 的环境变量和登录会话也不同。

**建议：** v1 明确“一份 agent 对应一个 OS 用户”，默认安装为 `systemd --user`/用户 LaunchAgent，以同一 UID 访问 herdr socket和启动 herdr daemon。配对信息记录 OS 用户和实际 socket 根目录。安装、status、logs、update、uninstall 都要按用户级服务设计；多 OS 用户一台机器暂不支持或要求各装一份。

### R17（高）：自建 DERP 的“默认 compose”目前不可直接成立

**风险/理由：** derper 需要公网域名、80/443 TCP 和 3478 UDP；可选 Caddy 通常也要占 80/443。很多依赖 Cloudflare Tunnel 或处在 CGNAT 后的自托管者无法提供 UDP 3478。长地址嵌入 DERP 主机，日后换域名/区域还会使已配对地址失效。

**建议：** 原则上推荐生产自建 DERP，但包装成显式 compose profile/附加 compose，而非无条件默认启动。文档给出 app 域名、DERP 域名、TLS 终止、端口占用和 STUN 的完整拓扑。公共 relay 只作显式试用。设计 DERP 地址迁移/重新配对流程，必要时在长地址中预留两个节点。

### R18（阻塞）：v1 实际是“单实例多用户”，不是可水平扩展服务

**风险/理由：** SQLite、内存 WS session、每 agent 一个 tailcat Client、同一 client key 不能多进程共用，这四点共同决定 v1 只能单实例运行。拓扑 A 每台在线 agent 都有完整网络引擎和 DERP 连接；切到拓扑 B 不只是替换接口，还涉及 agent 反向长连接、yamux 协议、密钥和升级迁移。

**建议：** 明确容量目标，例如“单实例、最多 N 用户/M 台配置主机/K 台持续在线”。S0 加 1/10/50 个 tailcat Client 的内存、goroutine、fd、DERP 连接、首连延迟和吞吐基准。若目标从一开始就是数百台常在线主机，应在 M1 前重新评估拓扑 B；不要把 A→B 描述成低成本透明切换。HA/多副本明确列为 v1 非目标。

### R19（高）：通知承诺意味着后台必须长期连接主机，但生命周期没有设计

**风险/理由：** 没有浏览器在线时仍要收到 blocked/done Web Push，服务端就必须持续维护每台订阅主机的事件流；对 tailcat 来说这也意味着持续保留每 host 的网络引擎。另一方面，“连续 9 次失败后只允许手动重连”不适合会休眠、换网或离线数小时的开发机，会让通知永久停止。

**建议：** 区分 metadata monitor 与可见 terminal：终端按需懒开；只有启用后台通知的主机保持事件监控。前台使用快速退避，后台离线后进入带 jitter 的慢速探测，永不永久放弃；用户操作/页面可见时立即重试并重置阶梯。为每用户设置常驻监控和连接配额。

### R20（高）：版本与升级策略不完整

**风险/理由：** 仅检查 `herdr ≥ 0.8.2` 不能防止未来私有 CLI/JSON 语义变化；tailcat 又要求 server/agent 成对锁依赖。agent 已作为 system service 安装，却没有更新、回滚、签名或兼容矩阵。

**建议：** 连接时做 capability/schema 探测，不只比较 semver；CI 至少覆盖最低支持版和当前稳定版。定义 server↔agent 协议版本、最低/最高兼容范围和强制升级提示。安装脚本固定版本并校验 SHA-256/签名，支持 `update --check`、回滚与卸载。未知 capability 默认降级或拒绝，不猜测。

### R21（高）：多用户 SSH 拨号天然形成 SSRF/内网扫描面

**风险/理由：** 任一注册用户都能让服务端连接任意 host/port，还能创建高成本 tailcat engine 和大量 SSH/terminal channel。开放注册时尤其危险，即使 SSH 握手不能直接取 HTTP 内容，也足以扫描实例所在内网并消耗资源。

**建议：** 邀请制之外仍需 egress policy：限制端口/网段的默认策略、DNS 解析后复核、防 rebinding、每用户 host/连接/终端/in-flight 请求/配对尝试配额、全局并发限制和审计。自托管管理员可显式允许私网；SaaS 模式应默认拒绝云 metadata 等敏感地址。

### R22（高）：AES-GCM 一句话不足以构成可运维的 secrets 方案

**风险/理由：** 随机 nonce、AAD、主密钥丢失、备份与恢复、轮换版本都没有定义。若数据卷里自动生成的 master key 没随 SQLite 一起备份，数据库备份不可恢复；反过来泄漏二者就泄漏全部主机凭据。

**建议：** 从首版使用版本化 envelope：`key_id + nonce + ciphertext + schema_version`，每次写入新随机 nonce，AAD 绑定 user/credential/kind。已有密文但 master key 缺失时必须拒绝启动，不能静默生成新 key。备份必须原子包含 SQLite、master key 与恢复说明；预留双 key 解密/新 key 加密的轮换结构，即使轮换 CLI 后做。SQLite 同时配置 WAL、busy_timeout、foreign_keys 和在线备份流程。

### R23（高）：远端终端输出必须按不可信内容处理

**风险/理由：** 恶意或被攻陷的主机会向 xterm 发送任意 ANSI/OSC。OSC 8 链接、OSC 52 剪贴板、窗口标题、图片协议和超长序列都可能变成浏览器侧钓鱼、数据覆盖或资源消耗入口。该系统又能直接输入远端命令，XSS 的后果很高。

**建议：** 上线前具备严格 CSP（尽量无 inline script）、依赖锁定、URL scheme allowlist、标题/label 长度限制、OSC 52 默认关闭或每次需用户手势、链接不自动打开、图片协议默认关闭、终端帧大小限制。审计日志不应等到 P2：M1 至少记录登录、失败认证、host key 接受/变化、凭据变更、配对/撤销、takeover 和破坏性 API；绝不记录终端输入输出。

### R24（中）：HTTPS、反向代理和 PWA 更新契约需要前置

**风险/理由：** Web Push、现代剪贴板和 PWA 在非 localhost 下都依赖安全上下文，因此 Caddy/HTTPS 不能只被描述成无影响的可选项。Service Worker 还可能缓存旧 JS，使升级后的前端与新 WS 协议短暂错配。

**建议：** 生产默认要求 HTTPS；纯 HTTP 只允许 localhost/可信内网并显示能力降级。明确 trusted proxy、Host/Origin 校验、上传大小、WS 超时与 buffering 配置。静态资源内容哈希，HTML network-first/no-store；Service Worker 更新时通过协议兼容检查再激活或提示刷新。

### R25（中）：协议单一来源与故障测试还没进入计划

**风险/理由：** Go 和 TypeScript 若各自手写 JSON/二进制结构，很快会漂移。该项目最难的部分恰恰是重连、乱序、缺帧、慢消费者与多客户端，而不是页面 CRUD。

**建议：** 选择 Go struct/JSON Schema/OpenAPI 中一个作为 wire 单一来源，生成 TS 类型和运行时校验；二进制协议用规范 + golden vectors。M1 前建立录制的 herdr trace、属性测试/fuzz、慢消费者、WS 半开、SSH 断开、agent 重启、server 重启、两客户端抢控制权等故障注入测试。

### R26（中）：Go 版本和仓库结构应简化

**风险/理由：** v0.3 同时写 Go 1.26+，但 tailcat 明确要求 Go 1.27。`/server`、`/agent` 加共享 `/internal` 也不如标准 Go 单模块布局直观。

**建议：** 仓库根使用单一 module `github.com/riba2534/herdrx`，明确 pin Go 1.27/toolchain；布局采用：

```text
cmd/herdrx/
cmd/herdrx-agent/
internal/
web/
deploy/
docs/
```

server 与 agent 统一版本发布；前端仍是独立 pnpm workspace，但产物由 server embed。

## 4. 产品与交互层面的修正建议

### R27（高）：SSH 接入能力要避免让用户误以为等同本机 `herdr --remote <alias>`

**风险/理由：** 用户当前的 SSH alias、ProxyJump、硬件密钥和 ssh-agent 都在用户电脑上；部署在远端的 herdrx-server 看不到这些配置。v1 的 host/port/user + 存储凭据只覆盖基础 SSH，不是完整 OpenSSH 兼容。

**建议：** v1 明确支持矩阵：直连 host/port/user、生成 key、OpenSSH 私钥、密码；是否支持加密私钥/passphrase和 keyboard-interactive也要写清。不承诺本机 alias、ProxyCommand、FIDO/GSSAPI。`HostConn` 应是能力接口（exec、streamlocal、keepalive、close），不要在架构上写死为 `*ssh.Client`，以便以后增加系统 OpenSSH/helper 适配器。

### R28（中）：“严格镜像 BSP”应指拓扑，不应指像素几何

**风险/理由：** snapshot/layout 里的绝对 rect 受 headless 尺寸或其他 client controller 影响，直接照搬会破坏响应式。手机本来就只显示一个 pane。

**建议：** 忠实范围定义为 pane 叶子顺序、split 方向、ratio、focus、zoom 和动作语义；浏览器根据自身容器重新计算像素。优先使用/验证 `layout.export` 的 BSP 树，不把原生 TUI 的 sidebar/tab 像素算进 Web 布局。

### R29（中）：TUI 直出不是天然“半天完成”

**风险/理由：** 对普通 sshd，申请 PTY 后运行 `herdr` 确实较简单；对 herdrx-agent，还需安全支持 PTY request、window-change、signal、退出状态和只允许固定命令。它也会成为新的 controller，和 Web 自绘模式竞争尺寸。

**建议：** S0 可先做一个仅开发者使用、仅 direct SSH 的 TUI 对照探针，帮助验证输入和重连。正式用户功能放 M2，并在 agent 的 PTY channel、权限边界和 takeover 测试通过后再承诺；暂时不要给“半天”工期。

### R30（中）：Cobalt2 精确还原与可访问性需要区分

**风险/理由：** xterm 的 `minimumContrastRatio` 会动态改写低对比颜色，开启后不再是“精确 Cobalt2”。状态若只靠红黄蓝绿区分，对色觉差异用户也不够。

**建议：** Cobalt2 仍可默认；把“原色模式”和“增强对比模式”设为明确选项，并默认同时显示图标/文字状态而非只用颜色。UI 状态色做 WCAG 对比测试。P0 不必打包庞大的完整 CJK WebFont，优先用系统等宽字体回退并做宽字符 golden test。

## 5. 十项产品与技术选择

| # | 我的意见 | 理由与条件 |
|---|---|---|
| 1. 注册模式 | **邀请制，且默认 closed 完成管理员 bootstrap 后再开启 invite。** | 符合自托管/可信小团队定位；“首个注册者自动管理员”必须改掉。开放注册只有在邮件验证、找回机制、egress/配额/滥用防护完成后才可作为显式选项。 |
| 2. SSH 私钥 | **接受生成 key + 上传私钥 + 密码，但生成 key 改为每主机一把。** | 每主机 key 便于独立撤销和轮换。密码默认只用于本次或由用户明确勾选保存；上传已有私钥是高级选项。普通 sshd key 权限较大，UI 要说清。 |
| 3. tailcat 配对 | **v1 接受“安装时预置白名单 + agent 出 QR + 服务端主动拨号”为唯一产品流程，但需补 agent SSH host key 固定与离线撤销。** | 这条路径让 agent 不必访问 herdrx 应用地址，适合拓扑 A。注意“tailcat 不支持 agent 反向”说法不准确：角色反转后 agent 可作为 Client 主动拨服务端，只是需要另一套长连接协议；若容量目标迫使选择拓扑 B，应重新拍板。 |
| 4. DERP | **接受生产推荐自建，不接受无条件默认启动。** | 自建更稳定、私密，但要求公网域名和 80/443 TCP + 3478 UDP，并与 Caddy 有端口规划。建议作为显式 compose profile；公共 relay 仅显式试用。 |
| 5. 手机首页 | **选 agents 列表，但保留明显的“继续上次终端”入口。** | herdr 的核心价值是快速处理 blocked/done，多主机用户也不应被自动带进一个可能立即 takeover 的终端。无 attention 时可以突出最近 pane，而不是改变信息架构。 |
| 6. 桌面布局 | **选严格 BSP 拓扑，不做自由布局。** | 维持 herdr 心智模型并避免双向布局冲突；“严格”限定为树、顺序、ratio、zoom 与操作语义，像素尺寸仍按 Web 容器响应式计算。 |
| 7. TUI 直出 | **要做，但拆成 S0 诊断探针和 M2 正式功能。** | 它是很好的协议对照与兜底；agent 路径并非半天，需要 PTY/resize/权限/控制权实现。 |
| 8. 主题 | **接受 Cobalt2 默认 + 6 个内置主题 + Latte 状态语义色。** | 同时要求文字/图标冗余表达、对比度测试，并把“精确原色/增强对比”作为设置；主题导入可后置。 |
| 9. 仓库与命名 | **用 monorepo，但改成标准 Go 单模块布局；module 取 `github.com/riba2534/herdrx`。** | 当前 origin 已是该 GitHub 仓库。推荐 `cmd/herdrx`、`cmd/herdrx-agent`、`internal`、`web`、`deploy`、`docs`，并统一 Go 1.27 与 server/agent 版本。 |
| 10. 多用户共享主机 | **v1 不做。** | 共享会引入 host ACL、凭据委托、终端控制冲突、审计和撤销语义，显著扩大风险。数据模型保留明确 owner 概念、控制租约按 host/pane 全局建模即可，不必提前做完整 membership。 |

## 6. 建议的 S0 技术验证退出标准

拍板后，S0 不应以“页面看起来能用”结束，而应至少产出以下可重复证据：

1. direct SSH 和 tailcat 各一台真实主机均能定位绝对 herdr socket，完成 snapshot、事件、observe、control、release。
2. 普通 shell、Claude/Codex 类全屏 TUI、alt-screen、mouse reporting、中文宽字符和大输出均能正确显示与输入。
3. 人为丢弃/延迟终端帧时，客户端能检测 gap 并通过 full snapshot 收敛，不出现永久花屏。
4. 慢消费者有明确内存上限；浏览器 xterm 处理完成能通过 Ack/credit 或等价机制反馈到服务端。
5. 浏览器刷新、WS 半开、SSH 断开、agent 重启、herdr daemon 重启分别有记录下来的恢复结果，并与 R2 的承诺一致。
6. 手机与桌面、两个浏览器标签页、原生 herdr TUI 同时打开同一 pane，控制租约、尺寸与 first-key 行为符合预期，不发生 resize 战争。
7. 验证 JSON focus/seen 是否影响其他 client，并据此定稿 Web 本地焦点与 Done 语义。
8. 新建、移动、关闭 pane/workspace 时，snapshot + events 投影不会回退或漏订阅。
9. tailcat 以 1/10/50 个 Client 测量内存、goroutine、fd、DERP 连接、重连和吞吐，为单实例容量写出数字。
10. agent 用与 herdr 相同的非 root OS 用户运行；命令白名单拒绝 shell 元字符、任意 herdr 子命令和越界 socket 路径。

S0 结束后应产出一份短 ADR：记录实测结果、失败假设、最终 wire/control/recovery 契约，并据此更新 `herdrx-plan-v0.4.md`。完成这些之后，再开始认证、CRUD 和完整 UI，返工风险会明显下降。
