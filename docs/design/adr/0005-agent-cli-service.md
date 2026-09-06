# ADR 0005: 公共 CLI 统一命名、Herdr 预检、IPC 单写者与后台服务管理

日期：2026-09-05（P1 v4 生产闭环修订版）。

## 决策背景与核心事实

在完成 P0 的基础安全门禁与受限网络验证后，P1 阶段确立了统一的产品命令行、服务端分发边界、可信受控端环境预检以及基于本地 IPC 与文件锁的单写者状态管理体系。

1. **二进制命名收归与兼容入口统一**：
   - **受控端 CLI 统一收归**：`cmd/herdrx` 成为用户使用的唯一客户端二进制，提供 `setup`, `connect`, `status`, `doctor`, `service`, `serve` 及旧命令兼容。
   - **Web 服务端独立入口**：当前服务端代码迁至 `cmd/herdrx-server`，保持 HTTP API、数据卷存储及健康检查（`herdrx-server healthcheck`）语义完全不变。
   - **兼容层入口统一**：`cmd/herdrx-agent` 作为薄包装直接复用 `internal/agentcli.Run`；旧命令 `run` 统一作为 `serve` 的兼容入口，完全共享同一 StateStore、同一排他锁、同一 IPC 与 `RunWithUpdater` 生命周期，彻底消除代码分裂与生命周期脱节。
   - **构建与镜像同步**：更新 `Makefile` 的 `dev` 与 `build` 目标；更新 `deploy/Dockerfile`，编译产物包含 `/out/herdrx` 与 `/out/herdrx-server`，容器内 `ENTRYPOINT` 与 `HEALTHCHECK` 同步指向 `/app/herdrx-server`。

2. **配置事务管理与锁所有权架构（StateStore）**：
   - **锁所有权模型**：由常驻 daemon 进程在启动时单次获取规范化配置锁（`configPath + ".lock"`，固定 inode，`O_NOFOLLOW`，校验属主与普通文件），并保存排他锁所有权；daemon 进程内部后续发起的任何 Update 事务（包括 IPC 请求发起的 unpair、SSH confirm-pair 等）直接复用已持有的锁并在内部互斥锁保护下串行执行，**彻底消除生命周期锁与事务锁重入死锁导致的 HTTP 500**。
   - **在线 CLI 必须走 IPC**：daemon 运行期间，CLI 命令（`pair`, `unpair` 等）优先通过 IPC 请求由 daemon 的 `store.Update` 统一执行并返回结果，绝不在外部争抢已持有的文件锁；仅当 daemon 离线时才降级为 CLI 本地排他锁保护写入。
   - **原子读改写事务（RMW）与父目录 fsync**：所有状态变更均经由候选文件写入 -> `tmp.Sync()` -> 原子 rename -> **父目录 fsync** -> 更新内存缓存，严格传播所有写入错误；实测 10 个并发 worker 增量更新 0 丢失。

3. **Legacy 配对完整性与实际执行贯通**：
   - **哈希一致性与在线配对**：统一使用标准库 `base64.RawStdEncoding.EncodeToString(secure.TokenHash(token))`，确保 `pair` 输出与 `confirmPair` 校验 100% 匹配；IPC 新增 `POST /legacy/pair`，支持在 daemon 运行中经由事务生成并返回完整 `#pair=` URL 与 QR 码。
   - **SSH 回调全链路贯通**：SSHServer 接入 `ConfigUpdater` 接口，收到 `confirm-pair <token>` 时直接调用 `store.Update` 事务完成状态落地与缓存更新，确保通过 IPC 查询 `status` 时立即看到 `paired=true`，彻底消除内存快照陈旧。
   - **真实 SSH 端到端闭环**：通过 `TestLegacy_FullWorkflowThroughIPCAndSSH` 完整验证了真实的 `init` -> `启动 serve (IPC+SSH)` -> `在线 CLI pair` -> `提取 URL token` -> `真实 SSH 客户端连入发送 confirm-pair` -> `IPC status 实时变为 paired` -> `IPC unpair 撤销` 的全链路真实端到端测试，彻底废除测试辅助函数假闭环！
   - **HerdrBin 注入真实 exec**：受控端 SSH 允许执行的 `herdr terminal session` 命令，将配置中的 `HerdrBin`（绝对路径）真实作为命令的可执行文件执行，经真实 SSH exec 验证支持路径带空格与非 PATH 自定义路径。

4. **Herdr 环境与运行状态多级严格预检（Preflight）**：
   - **拒绝假冒程序**：严格检查 `--version` 输出，必须包含有效 semver 版本，断然拒绝 `/bin/true` 等假冒程序。
   - **接口 Schema 严格校验**：执行 `herdr api schema --json`，解析 `.schemas.request.oneOf[].properties.method.const`，断言包含核心接口方法（如 `session.snapshot` 或 `workspace.list`）；返回空、非 JSON 或缺少关键方法一律判定为 `incompatible`。
   - **后台服务双重探活**：解析 `herdr status server` 的多行结构（必须明确为 `status: running`），并对其 socket 路径发起真实的 `net.DialTimeout` 探针测试；若 socket 无法连通，判定为 `not_running`。
   - **防挂起与真实报错**：外部命令执行器封装 3 秒超时与 1MB 输出上限，防止命令挂起；若状态为 `not_running`，setup 明确以退出码 1 失败并给出指引，绝不输出“setup completed successfully”。探测到的 Herdr 绝对路径持久化保存于配置中，供后续 daemon 和诊断沿用。

5. **本地 IPC 控制面与服务管理**：
   - **孤儿 Socket 安全清理**：在监听 `$XDG_RUNTIME_DIR/herdrx/control.sock` 前先通过 Dial 探活；仅当明确返回 `ECONNREFUSED`（连接被拒）、且文件类型为 `ModeSocket`、属主等于当前 UID 时才安全删除，拒绝误删普通文件或活跃实例 socket。
   - **强类型诊断**：`doctor` 接口使用类型明确的 `DoctorResult` 结构，避免类型断言失真；`status --json` 离线时退出码为 1（与文本模式一致）；连接地址在状态输出中严格脱敏。
   - **Systemd Runner 抽象**：抽象 `ServiceRunner` 接口，解耦生产与测试；真实安装时检查 `loginctl linger` 状态并如实告警；unit 文件注入绝对程序路径、`Restart=always`、`RestartSec=3`、`NoNewPrivileges=true` 及安全 `PATH`。

---

## 验证事实与数据记录

### 1. 单元与 `-race` 竞态测试实测（已跑，verify/p1-v4-agentcli-race.log）
- **测试命令**：
  `GOCACHE=<run>/scratch/go-cache GOMODCACHE=<run>/scratch/go-mod TMPDIR=<short_path> go test -race -v ./internal/agentcli/...`
- **退出码**：`0`（用时 2.135s，24 个测试用例在 race 探测下全部 PASS）。
- **具体用例与实测证据**：
  1. `TestStateStore_ConcurrentIncrementalUpdateNoLost`：10 个并发 worker 循环通过 `store.Update` 增量修改计数字段，断言最终计数精确匹配，证实 RMW 事务无 lost update；
  2. `TestConfigLock_SymlinkRefused` & `TestConfigLock_SameConfigMutualExclusion`：验证符号链接锁拒绝，同配置不同进程互斥生效，且释放锁后锁文件固定 inode 保持不变；
  3. `TestLegacy_FullWorkflowThroughIPCAndSSH`：真实端到端完整闭环，验证 `init` -> `启动本地 serve (IPC+SSH)` -> `在线 CLI pair 经由 IPC` -> `URL 解码` -> `真实 SSH 连入 confirm-pair` -> `IPC status 实时变为 paired` -> `IPC unpair 撤销` 全流程通过；
  4. `TestPreflight_BinTrueRejected`：验证 `/bin/true` 被坚决识别并拒绝为 `incompatible`；
  5. `TestPreflight_SocketDisconnected`：验证 status 返回 running 但 socket 断连时准确返回 `not_running`；
  6. `TestPreflight_FullSuccess`：验证合法版本、完整 schema 与真实监听的 mock unix socket 探活通过，返回 `ok`；
  7. `TestIPC_StaleSocketCleanup`：验证物理存在的孤儿 socket 安全清理与正常重连；
  8. `TestIPC_NonSocketFileRefused`：验证普通文件拒绝删除且内容保持原样；
  9. `TestCLI_StatusOfflineExitCode`：验证文本与 JSON 模式在 daemon 离线时均严格退出码 1；
  10. `TestInstallService_LingerWarningAndCallSequence`：验证 FakeRunner 调用顺序（WriteUnitFile -> DaemonReload -> EnableAndStart）及 unit 内容安全断言。

### 2. 真实 SSH HerdrBin 执行测试（已跑，verify/p1-v4-agent-test.log）
- **用例**：`TestHerdrBinCustomPathExec_WithSpaces`（用时 0.02s，PASS）；
- **实测证据**：配置路径含空格的可执行文件 `custom herdr bin with space/fake_herdr.sh`，经由真实 SSH 客户端连接发送 `herdr terminal session observe s1 --cols 80 --rows 24`，成功执行并返回 `custom-herdr-called: terminal session observe s1 --cols 80 --rows 24`，证实受控端命令执行真实接入了持久化的 HerdrBin 绝对路径。

### 3. 独立子进程级 Smoke 实测（`TestSubprocessSmoke_RealCLIAndIPC`）
- **可移植构建**：测试内部动态调用 `runtime.GOROOT()/bin/go` 从源码构建 `./cmd/herdrx` 至临时目录，绝不硬编码路径，绝不 `t.Skip`；
- **全流程实测证据**：
  1. 启动真实的 Mock Herdr 后台服务监听 `mock_herdr.sock`；
  2. 真实执行 `herdrx setup --herdr-bin <fake-cli> --config <cfg> --skip-service`，验证配置生成并持久化 `cfg.HerdrBin`，重复执行验证身份不漂移；
  3. 真实启动后台独立子进程 `herdrx serve --config <cfg>`（PID: 3699991）；
  4. **并发双开拦截**：尝试以同一 config 在不同 runtime-dir 启动第二个 daemon，立即由于文件排他锁被拒绝退出（退出码 1）；
  5. 真实从外部 CLI 执行 `herdrx status --json`，验证 IPC 通信成功，PID 严格匹配，Herdr 状态健康，地址脱敏；
  6. **在线 IPC 事务更新**：外部 CLI 执行 `herdrx unpair --config <cfg>`，成功经由 IPC 写入，彻底消除 HTTP 500 与死锁；随后 status 立即反映 `revoked=true`；
  7. 向子进程发送 `SIGTERM`，子进程优雅退出，`control.sock` 彻底从磁盘删除；
  8. **生命周期解耦断言**：断言独立运行的 Mock Herdr 后台服务在 `herdrx serve` 退出后依然正常连通并收发数据，证明接入组件退出绝不牵连或误停用户已有的 Herdr 进程！

### 4. 主树全量与构建实测（已跑，verify/p1-v4-full-test.log）
- **agent 模块回归测试**：8 组全 PASS（用时 2.211s）；
- **全量只读模块**：`go test -mod=readonly ./...` 全部通过（退出码 0）；
- **静态代码检查**：`go vet -mod=readonly ./...` 0 警告通过（退出码 0）；
- **二进制构建与健康检查**：成功构建 `scratch/bin/herdrx`、`scratch/bin/herdrx-server`、`scratch/bin/herdrx-agent`；`herdrx-server healthcheck` 退出码 0。

---

## 未实现项与生产待决 Blockers（Not Implemented / Blockers for Release）

1. **`herdrx connect` 临时配对端点与连接串生成**：P1 阶段明确拦截并返回未实现（HTTP 501），排期在 P2 实施；
2. **Herdr 独立运行**：移除未实现的 `setup --manage-herdr` 参数，Herdr 的安装与服务由用户自行管理；
3. **服务安装保护与真实就绪确认**：针对自定义 unit 不覆盖保护及生产级 systemctl 就绪探活，留待发布阶段闭环；
4. **安全在线更新与回滚**：排期在 P4 实现。
