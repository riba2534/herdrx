# ADR 0006: Tailcat 连接串导入、两阶段安全绑定与端到端终端闭环

日期：2026-09-05。
状态：**已修订并通过 A2 生产验收（Agent 侧生命周期与安全状态机就绪；Web 侧待 Web B 独立验收）**。

> **阶段事实与修复记录**：此前 A1/A3 历史回执中未经验收的过宽断言已正式撤回。在 A2 阶段，针对 Leader 隔离红例实证指出的生产缺陷进行了定点彻底修复：
> 1. **SetupID 安全哈希与迁移**：从基于序列化 CBOR 前缀改为基于稳定根公钥文本的 128-bit SHA-256 安全哈希（前缀 `setup-` 配合 32 位十六进制），彻底消除了跨实例碰撞。对旧未绑定 identity-only 配置提供了安全迁移路径，同时对已处于 prepared/active 绑定上下文的 AgentID 严格保持不变。
> 2. **PSK 双来源歧义消除**：在 `Config` 模型层与 StateStore 事务中实现了 `FormalPSK()` 与 `SyncPSK()`，强制对齐顶层 `PresharedKey` 与 `Node.Public.PresharedKey`，保证无论启动还是持久化均拥有明确非零的 Noise PSK。
> 3. **Daemon 统一生命周期与自动崩溃恢复**：`IPCServer` 在启动时先于对外服务通过 `restoreFromStoreLocked` 从持久化存储原子恢复 `prepared` 与 `active` 状态的 formal Server，彻底消除了进程重启后正式端点断链问题；同时杜绝了同一 NodeKey 双开 legacy 服务的脑裂风险。
> 4. **Prepare 幂等与冲突防御**：全量相同参数重试直接复用已有端点与地址，冲突请求与保存失败严格拦截且绝不误杀已有端点。
> 5. **SSH 认证角色锁定与禁止原地升权**：在 SSH 握手层通过 `PublicKeyCallback` 固化 `role`（`prepared` 或 `active`）与 `epoch`，在 prepared 状态建立的同一长连接即使后续执行 commit 成功也严禁执行终端命令与 streamlocal，必须以 active 身份重新发起握手；同时对 active 连接支持 commit 幂等重试且严格验签。
> 6. **并发 Channel 与长连接强断**：支持多并发 exec 与 direct-streamlocal 通道，动态探测运行平台架构，在受控端执行 `unpair` 时强制断开所有存活网络连接与正式端点。

## 决策背景与核心事实

本 ADR 记录 P2 阶段的纵向核心闭环：在受控端通过 `herdrx connect` 独立生成一次性受限连接字符串，在 Web 侧通过“添加主机 → Tailcat 内网穿透”粘贴导入，经两阶段原子状态机（prepare/commit）与真实 Ed25519 签名持有证明，建立加密绑定并打通 Web 终端交互链路。

1. **连接字符串格式与安全防伪（herdrx://v1/）**：
   - **格式规范**：采用 `herdrx://v1/<base64url(JSON)>`。整串长度硬限制为 `<= 16 KiB`，JSON 解析严格执行 `DisallowUnknownFields` 拦截未知字段。
   - **强制 PSK 机制**：连接串中的 Tailcat 地址必须包含非零的 256 位噪声预共享密钥（PSK），在解析时通过 `tailcat.ParseAddr` 严格核验 `!ci.PresharedKey.IsZero()`，坚决杜绝任何无 PSK 降级。
   - **展示与鉴权分离**：串内的 `host`, `os`, `arch`, `agent_ver`, `exp` 仅作为自报展示信息供前端界面呈现，授权边界与有效期完全由受控端本地时钟裁定。

2. **可信到期机制（Agent-side TTL）与临时端点管理**：
   - **受控端本地时钟裁定**：临时端点（Enrollment）与准备中（Prepared）状态的有效期由 Agent 本地生成并持久化（固定 10 分钟），严格杜绝由不可信客户端篡改时间。
   - **主动看门狗关闭**：临时 Server 启动内部到期定时器，窗口一过立即调用 `Close()` 销毁临时监听器并切断存活的 SSH 连接，不仅阻止新握手，且使得旧连接串彻底失效。
   - **跨越首次 TTL 的恢复能力**：一旦绑定正式进入 `active` 状态，后续凭借正式私钥发起的重连、状态查询或 commit 幂等重试不再受首次 10 分钟窗口限制。

3. **两阶段物理隔离握手协议（Prepare / Commit）**：
   - **临时通道（Ephemeral Endpoint）**：由受控端按需拉起，拥有独立的临时 Key 与 PSK，白名单 `AllowedClients` 严格只允许该单次临时客户端；关联的 SSH 服务仅放行 `pairing-prepare` 与 `pairing-status`，物理封禁终端 exec、PTY、Unix socket 转发与 commit。
   - **正式端点启动**：受控端在临时通道收到合法的 `pairing-prepare` 并提取 Controller 正式公钥后，在磁盘上将状态原子记录为 `prepared`，启动只允许该正式公钥的正式端点（Permanent Server），并将其真实地址返回给 Web。
   - **正式通道数字签名激活（Commit）**：Web 端使用正式私钥拨号正式端点，发送 `pairing-commit` 及对上下文消息（`challenge + binding_id + controller_id + agent_id + enrollment_id + formal_client_node + ssh_fp + formal_addr`）的真实 Ed25519 签名；受控端验证签名通过后，原子持久化 `status=active`，递增 `epoch`，销毁临时端点与临时材料，正式通道放行终端控制。

4. **Web 异步任务架构与 SSRF 白名单防御**：
   - **异步导入任务**：Web 侧 `POST /api/tailcat/enrollments` 返回 `202 Accepted` 与任务 ID；前端通过 `GET /api/tailcat/enrollments/{id}` 轮询进度（`verifying` -> `connecting` -> `preparing` -> `committing` -> `active`）；页面刷新时不重复创建主机。
   - **严格 SSRF 校验**：解析连接串与返回的正式地址中的 DERP 节点主机与端口，调用 `SSRFValidator` 严格拦截 `127.0.0.1`、`169.254.169.254`（云元数据）、链路本地与未授权私网，防范 DNS rebinding；仅在测试环境下通过显式白名单放行本地 DERP 端口。
   - **秘密隔离与加密存储**：Web 生成的正式私钥与受控端正式地址经由 AES-GCM 加密存储于 `credentials` 表中；公开的 `hosts` 列表/详情 JSON 绝不回显私钥或未脱敏的 PSK 地址。

5. **Web 前端页面交互实现**：
   - 在添加主机弹窗中集成“Tailcat 内网穿透”；提供主机名称（可选）与连接字符串 `<textarea>`；
   - 带有清晰的 10 分钟有效与长期绑定说明；展示分步进度指示，防重复点击，提交成功后直接打开 `/h/<host_id>` 终端工作台。

---

## 验证事实与数据记录

### 1. 核心网络与协议实测（已跑，verify/ 归档）
- **internal/tunnel**：
  - `TestSSRFValidator`：验证回环、云元数据及私网拦截通过；
  - `TestConnectionString_BuildAndParse`：验证标准生成、解析、无 PSK 拦截与超限拦截通过；
  - `TestTwoPhaseProtocol_LiveDERPEndToEnd`：在自建本地 DERP 下验证了临时端点拨号、`pairing-prepare` 握手、正式端点启动、Ed25519 签名 commit 激活及激活后 `uname` 执行（耗时 0.11s，PASS）。
- **internal/httpapi**：
  - `TestTailcatEnrollment_SSRFAndSizeValidation`：验证请求体超过 16KiB 拦截（HTTP 400）、元数据地址拦截（HTTP 400）与回环地址拦截（HTTP 400）；
  - `TestTailcatEnrollment_EndToEndWithLocalDERP`：真实全链路闭环，受控端生成连接串 -> Web 发起异步导入 -> 自动完成临时通道与正式通道两阶段握手 -> 数据库加密落盘 -> `hosts.Open` 真实打开终端连接（耗时 0.35s，PASS）。
- **internal/agentcli E2E 纵向实测（A2 全量 PASS，-race）**：
  - `TestSetup_AgentIDUniquenessAcrossInstances`：验证 3 个独立配置的 CLI 子进程生成全局唯一的 128-bit 哈希 SetupID，且重复 setup 幂等不漂移；
  - `TestCLI_ConnectPrepareCommitAndHerdrIO_FullPipeline`：源码构建 CLI 启动 serve、connect 拿串、临时与正式端点两阶段握手、commit 激活、Herdr 终端 stdin/stdout 双向流动与 direct-streamlocal 结构化转发闭环；
  - `TestCLI_CrashRecovery_PreparedRestart`：prepared 状态杀死 daemon 并启动新 PID 实例，验证自动恢复 formal Server、prepared 状态锁定、commit 激活、**同连接绝不原地升权拦截**与新建连接终端执行；
  - `TestCLI_CrashRecovery_ActiveRestart`：active 状态杀死 daemon，验证新 PID 实例自动恢复 formal Server 与终端交互，并验证再次杀死后第三个 PID 实例依然成功恢复（两次连续重建验证）；
  - `TestCLI_Security_IdempotencyAndConflictAndReject`：验证全量相同参数 prepare 幂等复用、冲突 prepare 严格拦截、未授权 SSH 公钥在握手阶段拦截、commit 丢包在 active 连接上幂等重试通过与伪造签名拦截；
  - `TestCLI_RevokeAndReEnroll`：验证在线执行 unpair 强制切断存活 SSH 长连接并关闭正式端点，撤销后再次执行 connect 开启全新 enrollment 周期（递增新 epoch）并成功完成新 Controller 绑定。
- **internal/agent & internal/agentcli 单元与服务测试**：
  - 在 `-race` 下全量测试全部 PASS（包含 HerdrBin 空格路径真实 SSH exec 执行、进程排他锁、并发 RMW 事务、IPC status/doctor 通信与独立 Herdr 进程解耦）。
- **前端 Web 状态说明**：
  - 静态包构建与基础单元测试已通过，端到端导入与页面级联动按契约安排在 Web B 阶段独立验收，本 A2 阶段不扩散。

### 2. 模拟与隔离测试说明（Mock vs Real）
- 在自动化测试中，使用了本地回环 Unix socket 模拟真实 Herdr 进程的请求响应，以验证协议路由与生命周期解耦，绝未影响当前运行的宿主 Herdr pane。
- 所有测试运行在 `scratch/` 隔离目录下，未改动系统真实配置与服务。

### 3. 未运行项说明（Not Run）
- **跨公网真实 NAT 穿透与 72 小时多机容量测试**：**Not Run**。本阶段所有网络验证均在隔离本地回环 DERP 上完成，真实的跨 NAT/跨地域网络穿透与 soak 压力测试需在后续具备测试服务器集群时实施。
- **真实系统级 systemctl 启停与开机服务注册**：**Not Run**。契约严格只读且严禁操作宿主系统服务，通过测试 Runner 隔离完成。
