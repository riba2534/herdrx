# herdrx 项目指引

本文件是项目级 Agent 指引的唯一正文。`AGENTS.md` 必须是指向 `CLAUDE.md` 的相对符号链接；修改指引时只编辑本文件。

## 项目是什么

herdrx 是 Herdr 的多用户 Web 客户端，提供电脑和手机上的多主机工作台、xterm 终端及 Agent 状态查看。目标用户是可信管理员管理的个人或小团队，首发为单实例、Linux amd64/arm64。网站不负责下载、升级或停止用户的 Herdr。

统一称呼：**工作台主机**是部署 herdrx 网站/API/数据库的机器；**远程主机**是用户通过工作台接入、独立运行 Herdr 和任务的其他机器；**浏览器**是访问工作台的客户端。避免单独使用“服务器”混指前两者。Herdr 服务端运行于远程主机，herdrx Web 服务端运行于工作台主机。

**远程主机上的 Herdr 独立运行，Web 工作台是查看和交互入口。** 浏览器关闭、刷新、断网，Web 登录退出、过期、用户禁用，以及网站服务重启，只影响对应的访问连接；工作台主机整机断电、宕机或被删除，同样不能停止远程主机上的 Herdr、销毁其 workspace/tab/pane、结束 pane 内的 Shell/Agent/任务。重新进入工作台应连接原有会话，恢复查看当前状态和 Herdr 保留的历史，不重复创建任务或重放输入。

远程 Herdr、PTY 和任务的存活不能依赖工作台主机的进程、SSH 连接、隧道或心跳；没有工作台也应能在远程主机上直接使用 Herdr。工作台离线影响经由它的访问和通知，不改变远程任务的执行生命周期。已有本机接入作为辅助模式，与网站同机时共享主机故障边界，不能用它代替远程主机独立性验收。

文档和代码必须区分“Web 登录会话”“Web 终端观察流”和“Herdr 会话”。清理 WebSocket、SSH 通道或观察客户端进程属于断开访问；只有用户显式执行并确认关闭 pane/tab/workspace 等操作时，才允许调用对应的破坏性 Herdr API。浏览器离开和认证撤销路径不得隐式调用这些 API。

最终实施方案见 [远程任务独立运行与访问管理](docs/design/auth-admin-remediation.md)，包含连接边界、登录注册修复、管理员功能及独立主机验收；方案条目在实现和验收前保持待完成状态。

- 网站入口：`cmd/herdrx-server`。
- 受控机 CLI：`cmd/herdrx`；`cmd/herdrx-agent` 是兼容包装。
- 接入方式：原生运行时的本机 Herdr、基础 SSH、Tailcat。
- `../herdr`、`../tailcat`、`../orca`、`../paseo` 是只读参考仓库。产品改动只写本仓库。

## PWA 客户端要求

**herdrx 必须能通过浏览器安装到 Mac 和手机，作为独立窗口 Web App 日常使用。** 提供统一的应用名称、图标与安装后的启动入口；客户端验收覆盖 Mac，以及 iOS / Android 手机。

- 项目默认仅提供 HTTP，不附带 HTTPS、证书管理或反向代理；本机测试使用 HTTP/IP 或 localhost，不要求域名。部署者自行选择是否通过自己的反向代理提供 HTTPS。
- PWA/Push 按浏览器支持的安全上下文启用，本机可用 localhost 验证 Service Worker。安装到真实 Mac/手机的记录单独保留，不以正式域名或 HTTPS 部署作为本轮发布前置条件。
- UI、认证、通知和更新改动要兼顾浏览器标签页与已安装 PWA。关闭或退出 PWA 继续遵守远程 Herdr/任务独立运行的边界。
- manifest、Service Worker 或手机布局存在只代表具备基础实现；真实 Mac/手机安装未执行时如实记录，不能称为已验收。
- PWA 安装确认和权限请求由浏览器/系统提供；应用内业务选择框与提示使用自绘组件。该要求针对 Web 客户端，受控端 CLI 平台范围另行定义。

## 已确认的部署架构

1. 源码推送到 GitHub `main` 后，由 GitHub Actions 自动执行验证和 Docker 构建。
2. 验证通过的 Linux amd64/arm64 网站镜像发布到公开 Docker Hub `riba2534/herdrx`；发布凭据仅通过 GitHub `dockerhub` Environment Secret `DOCKERHUB_TOKEN` 提供。
3. `latest` 指向成功发布的当前主分支构建；同时保留提交标签和 digest。旧提交手动重跑不能将 latest 倒退。
4. README 推荐工作台主机使用公开镜像一键 `docker run` 部署，更新时 pull 后重建同名容器并保留原数据目录。CI 不自动登录工作台主机部署；这条网站发布链路不部署或重启远程主机上的 Herdr。
5. **持久化一律使用可见的本地目录 bind mount。禁止命名卷、匿名卷和 Dockerfile 的 `VOLUME` 指令。** 应用数据为部署目录 `./data`，可选 DERP 证书为 `./derp/certs`。
6. `deploy/compose.yml` 保留为可选 Compose 部署定义，不包含 `build:`；`compose.prod.yml` 是它的相对符号链接。`compose.dev.yml` 仅供开发验证，不在 README 推荐源码部署。
7. 镜像默认以 `65532:65532` 的 nonroot 用户运行。首次 Docker 部署使用 `sudo install -d -m 700 ./data && sudo chown 65532:65532 ./data` 初始化目录；可选 Compose 使用 `deploy/prepare-data.sh`，不能靠 `chmod 777` 解决权限。
8. Docker Hub Token 等凭据只进入 GitHub Environment Secrets 或本机受限配置文件。公开源码、README、日志和附件不得包含私人域名、内网地址、个人邮箱、绝对用户目录或真实凭据；私人地址示例使用 example.com/test；公开 Docker Hub 引用可直接写入发布附件。
9. 私人交接与运行记录放在已忽略的 `.local-notes/`，不纳入公开设计文档；公开 GitHub / Docker Hub 仓库标识、Go module 和兼容服务标识可以保留。提交前检查 Git 候选文件及 staged 内容。

## 代码布局

| 路径 | 职责 |
|---|---|
| `cmd/` | 网站及 CLI 命令入口 |
| `internal/httpapi` | HTTP、认证、主机管理、配对任务、终端 WebSocket |
| `internal/store`、`internal/secure` | SQLite、凭据加密、密码与密钥 |
| `internal/herdr`、`internal/hostruntime` | Herdr 协议适配与主机连接 |
| `internal/agent`、`internal/agentcli`、`internal/tunnel` | 受控端、IPC、服务管理、Tailcat 与绑定状态机 |
| `internal/updater` | CLI 签名更新及回滚 |
| `web/src` | React/TypeScript/xterm 前端 |
| `internal/webassets/dist` | Go embed 前端资源，由 `make web-build` 更新；仅 `.gitkeep` 进入 Git |
| `deploy/`、`.github/workflows/`、`scripts/` | Docker、发布及部署验收 |
| `docs/` | 安装、运维、设计决策与发版验收 |

## 开发与验证

工具链：Go `go.mod` 声明的版本（当前 1.27.1）、Node.js 26.4.0、pnpm 11.25.0。前端版本以 `web/package.json` 和锁文件为准。

```bash
pnpm --dir web install --frozen-lockfile
make web-build
go run ./cmd/herdrx-server
```

网站默认 `127.0.0.1:8080`。`go run ./cmd/herdrx` 是 CLI，不是网站。修改前端后重新构建 embed 资源，避免启动旧页面。

```bash
go vet ./...
go test -count=1 -timeout=10m ./...
go test -race -count=1 -timeout=10m ./...
pnpm --dir web typecheck
pnpm --dir web lint
pnpm --dir web exec vitest run
python3 scripts/check-deployment.py
python3 scripts/test-publish-images.py
python3 scripts/test-cli-installer.py
# 浏览器回归：先构建网站二进制，安装浏览器后执行
pnpm --dir web exec playwright install chromium firefox webkit
node scripts/test-browser-auth.mjs /absolute/path/to/herdrx-server
HERDRX_TEST_ENGINES=chromium,firefox,webkit node scripts/test-tailcat-onboarding.mjs
HERDRX_TEST_ENGINES=chromium,firefox,webkit node scripts/test-keys-folders.mjs /absolute/path/to/herdrx-server
HERDRX_TEST_ENGINES=chromium,firefox,webkit node scripts/test-app-appearance.mjs
HERDRX_TEST_ENGINES=chromium,firefox,webkit node scripts/test-custom-controls.mjs
node scripts/test-terminal-rendering.mjs
HERDRX_TEST_ENGINES=chromium,firefox,webkit node scripts/test-workbench-display.mjs
```

Docker 变更需构建候选镜像并运行 `python3 scripts/smoke-image.py <镜像> [预期版本]`。脚本需要当前用户能使用 Docker 和无交互 sudo，仅操作隔离临时目录和测试容器。

按改动选择必要检查。影响发布流水线时应跑完整门禁；测试失败先定位原因，不用跳过测试或隐藏错误获得绿色状态。

## 实现约束

- 所有主机、凭据与配对任务操作检查 owner；实例管理员能接触终端明文，这是产品信任边界。
- 临时配对凭据只能调用配对子协议；不能通过同一连接原地升级权限。
- 身份、PSK、binding、epoch 的持久化和撤销是核心数据。迁移失败不能悄悄生成新身份，回滚不能复活已撤销授权。
- 慢网络请求不持全局锁；终端输入结果不确定时不自动重放；共享连接变更必须考虑并发、重启及失效恢复。
- 保留 Herdr 的 workspace/tab/pane/BSP 心智模型；原版 TUI 嵌套入口已退役。
- 本机、SSH、Tailcat 的终端操作、中文、Shift+Enter、滚动、分屏和图片粘贴需保持一致。
- 用户界面及项目文档优先简体中文；错误应说明用户下一步可执行的动作，不能推荐未实现的命令。

## 协作与文档

- 用户本次明确指令优先于历史方案。文档中的设计不代表已实现或已验收。
- 先查看工作区状态，保留已有未提交改动；不擅自清理文件、重置分支或改动其他项目。
- 普通开发与验证可自主完成。提交/推送、切换用户正在体验的实例、真实服务器故障注入按用户授权范围执行。
- 使用隔离测试目录、端口和 Herdr session；不要用用户已有 pane 测试断网、撤销、崩溃或升级。
- README 只面向使用者：中文介绍、功能、一键 `docker run`、主机接入、日常使用、配置、更新与 FAQ；不写架构、开发、测试或维护者发布流程，不推荐源码部署。操作细节放入对应文档，不把历史交接和未完成设计写成已交付能力。
- 部署行为变更同步 `README.md`、`docs/install.md`、`docs/operations.md`、`docs/update-and-recovery.md` 和发布说明。
- 发版差距见 `docs/release-readiness-2026-09-06.md`。分别标注已复现问题、静态发现与未执行验收；不能把短时冒烟等同于真实跨网或 72 小时测试。
