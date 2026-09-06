# ADR 0004: Tailcat P0 风险验证、能力门禁与两阶段配对协议

日期：2026-09-05（P0 最终处置归档版）。

> **状态声明**：依据 `p0-final-disposition.md`，本 ADR 记录 P0 阶段的技术可行性与受限门禁实测基线。生产配对协议尚未放行上线，原型中的网络模型与两阶段状态机仍有待关闭门禁（见文末说明），留待 P2 完整闭环；本文件不构成全产品发版放行依据。

## 决策背景与核心事实

1. **主树安全门禁与角色绑定机制（Permissions 隔离）**：
   - **认证时刻角色锁定**：为消除握手后读取磁盘导致的并发提权竞态，连接角色必须在 `ServerConfig.PublicKeyCallback` 中原子读取配置并写入 `ssh.Permissions.Extensions["role"]`（`unpaired` 或 `paired`）。`Handle` 与通道处理严格读取本连接的 Permissions 角色，不再猜测认证后磁盘状态。
   - **禁止原地提权**：未配对连入的会话在此连接生命周期内只能执行 `confirm-pair`，即使在同一连接内配对成功，后续命令依然被拒绝；必须断开并重新建立正式 SSH 握手方可执行日常管理。
   - **严格协议层拒绝（Reply(false)）**：未授权命令在 `Session.Start` 协议层即被直接拒绝，不误发 `Reply(true)`。
   - **StreamLocal 物理封禁**：在 Channel 建立阶段显式校验配对状态，未配对时直接返回 `ssh.Prohibited`。
   - **Revocation 真实断连**：`watchRevocation` 监测到撤销后关闭底层连接，客户端 `client.Wait()` 捕获真实 `EOF`；随后发起新握手直接报错 `agent access is revoked`。

2. **Tailcat v0.6.0 机制与 PSK 持久化实测**：
   - 依赖事实：`tailcat v0.6.0` 默认启用了 256 位 WireGuard 预共享密钥（PSK），并将依赖基线提高至 Go >= 1.27.1。虽有 `DisablePresharedKey` 兼容开关，但为保证长期安全性，新协议应默认启用 PSK。
   - 隔离实测事实：
     - 在 127.0.0.1 隔离本地自建 DERP（Region 1）上启动真实 `tailcat.Server`；
     - 完整恢复长期持久化 `NodeKey` 与 `PresharedKey` 时，新 Server 的 `TailcatAddr()` 100% 保持一致，客户端使用原地址重连并成功通信；
     - **孤立 PSK 改变负例**：先关闭旧节点确保注销，启动恢复了相同 NodeKey 但丢弃 PSK 的 Server，并配置相同端口的有效 echo 服务。实测表明：新地址能正常收发（证明服务与节点健康），而使用原地址拨号必定失败（证实客户端失联仅由 PSK 不匹配造成）。
   - 主树依赖边界：主树当前 `go.mod` 保持锁定在 `v0.5.0` 与 Go 1.27，不在 P0 盲目变动生产依赖；v0.6.0 适配实测全部在 `scratch/p0-prototype` 独立模块中完成。

3. **双端点物理隔离（Ephemeral vs Permanent）**：
   - 临时端点（Ephemeral）：动态拉起，拥有独立短期 NodeKey 与临时 PSK，启动前严格配置 `AllowedClients` 白名单；客户端通过 `waitForHandshake`（初始握手探针，不暗示 P2P 直连）完成连接；其关联的 SSH 服务仅支持 `pairing-prepare` 与 `pairing-status`，拒绝在临时端点完成 commit，物理封禁 streamlocal 与系统命令。
   - 正式端点（Permanent）：恢复长期身份，仅允许正式绑定的 Web Controller 公钥连入，支持正式终端与 API 转发。

4. **两阶段配对（Prepare / Commit）与崩溃恢复状态机**：
   - **全量唯一键与输入校验**：绑定记录绑定 `EnrollmentID`、`RequestID`、`ControllerID`、`FormalClientNode` 与 `FormalSSHPublicKey`；节点公钥调用真实 parser 解析并拒绝零值；短/空 ID 及过期输入明确报错。
   - **时间绑定由受控端决定**：`prepared` 到期时间来自受控端可信记录并在磁盘持久化；在 `Commit` 时严格检查当前时间是否超过有效期，超时坚决拒绝。
   - **Commit 幂等分支强制先验签**：在状态变为 `active` 后，幂等重试同样先验证正式私钥对上下文消息的真实 Ed25519 签名，防范非授权查询。
   - **冲突拦截与撤销粘性**：处于 `prepared` 状态时拒绝任何不同参数的竞争 Prepare；处于 `revoked` 状态的绑定严禁复活。
   - **Candidate 落盘模式与父目录 fsync**：状态机必须先将 candidate 原子写入临时文件、fsync、重命名，并执行父目录 fsync；落地成功后方可更新内存状态；若磁盘写入失败，内存保持原样不变（经保存失败注入测试验证）。注：父目录 fsync 保证了文件系统元数据落盘，但操作系统在极度异常断电下的完整恢复仍依赖底层存储硬件屏障。

---

## 验证事实与数据对照

### 1. 主树真实实测（已跑，verify/p0-v3-agent-test.log）
- **测试命令**：
  `GOCACHE=<run>/scratch/go-cache GOMODCACHE=<run>/scratch/go-mod TMPDIR=<short_path> go test -v ./internal/agent/...`
- **退出码**：`0`（用时 1.047s，5 组测试全部 PASS）。
- **具体用例与实测证据**：
  1. `TestStreamLocal_PairedGateControlAndPass`：
     - 在临时目录下启动真实的 Mock Herdr unix domain socket（`herdr/herdr.sock`）；
     - 未配对时请求该合法路径：SSH 层直接拒绝（`requires an active paired agent`），且 mock socket 后端收到连接数为 `0`；
     - 新建已配对正式连接：channel 成功打通，双向收发 echo 数据完全一致，后端收到连接数为 `1`（证明功能不退化）。
  2. `TestUnpairedExecDenied_HerdrStageImageAndShell`：
     - 对未配对连接使用 `Session.Start` 启动 `herdr terminal session observe`、`herdrx-stage-image png`、`uname -sm`、`config-path`、`PTY-req` 及 `Shell`，断言全部在协议层启动时被拒绝（`err != nil`）。
  3. `TestPairingRoleFixed_NoInPlaceEscalation`：
     - 错误 token confirm-pair 失败，配置保持 unpaired；
     - 有效 token confirm-pair 成功，配置更新为 paired；
     - 在同一连接中继续调用 `Session.Start("uname -sm")` 仍然被协议层拒绝（杜绝原地提权）；新建正式连接后方可放行。
  4. `TestPairingRoleFixed_ConcurrentPairingRace`：
     - 包装 `PublicKeyCallback` 模拟并发配对竞态（在认证通过后、握手完成前修改磁盘配置为 Paired=true）；
     - 断言：由于连接角色直接绑定在 `Permissions["role"]`，该连接依然被锁定为 unpaired，`Session.Start("uname -sm")` 被协议层拒绝，而 `confirm-pair` 允许通过。
  5. `TestRevocationClosesActiveConnectionAndBlocksNewHandshake`：
     - 触发 `Unpair` 后，客户端 `client.Wait()` 在 1.00 秒后捕获到服务端关闭引发的真实 `EOF`；随后发起新握手直接报错 `agent access is revoked`。

### 2. 原型隔离网络实测（已跑，verify/p0-v3-proto-test.log）
- **测试命令**：
  `GOCACHE=<run>/scratch/go-cache GOMODCACHE=<run>/scratch/go-mod TMPDIR=<short_path> go test -v ./...`
- **退出码**：`0`（用时 1.722s，5/5 测试用例全部 PASS）。
- **具体用例与实测证据**：
  1. `TestTailcatLiveDualEndpoint_IsolationAndPSKRecovery`：
     - 在 `127.0.0.1` 启动隔离本地自建 DERP + STUN 服务（Region 1）；
     - 启动 `ephemeralServer` 与 `formalServer`，客户端经 `waitForHandshake` 初始握手通过隧道收发正常；
     - 未在白名单中的客户端拨号被服务端丢弃，超时被拒；
     - 关闭临时 Server 后，正式 Server 依然正常收发数据；
     - 关闭正式 Server，重启并完整恢复 `NodeKey` + `PresharedKey`：新 Server 的 `TailcatAddr()` 100% 相同，重连成功；
     - **孤立 PSK 负例实测**：先关闭旧节点，启动只恢复 `NodeKey` 丢失 `PresharedKey` 的 Server（配置有效 OnTCP echo）；实测证明新地址拨号能正常通信，而使用原地址拨号必定失败（证实是 PSK 改变造成的失联）；日志严格脱敏。
  2. `TestStateMachine_TwoPhaseCommitWithRealSignature`：
     - 验证短/空 ID、非法公钥、过去到期时间的防御性拒绝；
     - 验证 Prepare 幂等性与冲突拒绝；
     - 模拟进程崩溃重启从磁盘恢复 `StatusPrepared`；
     - 提交伪造签名被拒绝；提交真实 Ed25519 签名成功激活；
     - 验证 active 状态下伪造签名重试依然被拒绝，合法签名重放成功；
     - 验证撤销粘性且拒绝新 Prepare。
  3. `TestStateMachine_CommitSaveFailure`：
     - 注入 Commit 磁盘保存失败，断言内存状态保持 `StatusPrepared`，重建 SM 磁盘状态依然为 `StatusPrepared`；解除故障后重试成功激活为 `StatusActive`。
  4. `TestStateMachine_PreparedExpiration`：
     - 验证可信到期时间窗口过后，提交有效 Commit 签名依然被拒绝（`binding has expired`）。
  5. `TestEphemeralEndpoint_NarrowProtocol`：
     - 临时端点 SSH 物理拒绝 streamlocal、uname 与 commit，仅允许 prepare 与 status。

### 3. 未运行项与生产待关闭门禁（Not Run / Pending for P2）
- **跨公网 DERP 穿透与容量测试**：**Not Run**。本阶段所有网络验证均在隔离本地回环 DERP 上完成，真实的跨 NAT/跨地域网络穿透与 72 小时多实例 soak 压力测试安排在后续具备测试服务器集群时实施。
- **真实主机 systemd 用户服务**：**Not Run**。契约只读且严禁操作宿主全局 systemd 用户空间。
- **生产配对未放行事项（P2 必决项）**：
  1. 正式 SSH Handler 必须在认证与通道层严格校验仅放行绑定公钥，prepared 状态严格仅限 commit/status；
  2. 临时端点到期必须主动关闭存活连接与临时 Tailcat Server；
  3. enrollment 到期由受控端创建并持久化，不接受未校验的客户端时间；
  4. active 状态下即使首次 enrollment 窗口关闭，仍需支持凭借正式密钥持有证明的断线重连与恢复；
  5. 正式地址必须由本机启动的真实 Server 产生并完成全上下文绑定；
  6. 生产密钥输出脱敏、单 owner 唯一约束与 SSRF 出站防护。
