# herdrx Tailcat 接入与长期运行方案 v0.5

> 日期：2026-09-05（Asia/Shanghai）
>
> 状态：调研完成，方案待评审。本次仅新增本文，未修改业务代码、参考仓库或运行服务。
>
> 范围：Docker 分发网站、Go CLI `herdrx`、服务器主动生成连接串、后台运行、断线恢复、安全更新。
>
> 本文是 [v0.4 可执行方案](herdrx-executable-plan-v0.4.md) 的 Tailcat 专项修订；其中旧的“网页先生成初始化命令”流程由本方案替代。以下新增命令、接口、镜像标签均为设计，不代表已经发布。

## 1. 结论与推荐决策

这个使用场景可以实现，而且不需要重做终端系统。继续让 Herdr 管理 workspace、tab、pane 和实际进程；herdrx 负责网页、连接授权及远程传输。

推荐交付形态是：**一个 Docker 网站 + 每台受控服务器上的一个 Go CLI `herdrx`**。普通用户不需要安装 Go、单独安装 Tailcat、注册 Tailscale 账号，或配置系统 SSH 公钥。

核心决策如下：

| 问题 | 推荐方案 | 理由 |
|---|---|---|
| 谁先开始配对 | 服务器运行 `herdrx connect`，网页粘贴导入 | 符合用户操作顺序，不再把网站参数来回复制到服务器 |
| 连接串是否永久有效 | 10 分钟、单次授权；绑定身份长期有效 | 长期使用不等于长期暴露一份可抢占的凭据 |
| 后台进程 | Linux systemd 用户服务；检查 linger | 登出、断网、程序异常后可恢复，不依赖一直开着 SSH 窗口 |
| 如何更新 | `herdrx update`，签名校验、原子切换、失败回滚 | 一条命令完成，不重新配对，不联动重启 Herdr |
| P2P 不成功怎么办 | 自动使用 DERP 加密中继，并显示当前路径 | NAT/防火墙条件不同，不能承诺所有网络都能直连 |
| 网站与 CLI 命名 | CLI 为 `herdrx`；容器内网站为 `herdrx-server` | 满足用户命名要求，避免现有服务端二进制重名 |
| 首发范围 | Linux amd64/arm64；可信个人或小团队、单实例 | 先把真实服务器场景做完整；macOS 后续适配，Windows 暂不承诺 |
| 自动更新 | 默认只提示，手动一键更新；自动应用为后续显式选项 | 不在用户运行任务时擅自改变远程接入组件 |

需要明确：这里的 P2P 是 **网站的 Go 后端与受控服务器之间**，不是浏览器直接与服务器建立 Tailcat 隧道。实例管理员仍然是可信方，能够接触终端内容与主机凭据。

## 2. 最终用户体验

### 2.1 网站部署

用户下载发布包中的 `compose.yml` 和 `.env.example`，填写网站访问地址、选择一个已发布版本，执行：

```sh
docker compose up -d
```

生产 Compose 只引用发布镜像，**不含 `build:`**，不要求用户克隆源码或在本机编译。网站首次启动创建管理员，之后邀请成员，延续现有认证机制。

建议的 Compose 形态如下；`HERDRX_VERSION` 必须填写实际发布的版本，不能把本文示例理解成镜像已上线：

```yaml
services:
  herdrx:
    image: ${HERDRX_IMAGE:?请填写已发布镜像的完整引用}
    restart: unless-stopped
    ports:
      - "${HERDRX_PORT:-8080}:8080"
    environment:
      HERDRX_ADDR: 0.0.0.0:8080
      HERDRX_DATA_DIR: /data
      HERDRX_PUBLIC_URL: ${HERDRX_PUBLIC_URL:?请填写网站访问地址}
      HERDRX_COOKIE_SECURE: ${HERDRX_COOKIE_SECURE:-true}
    volumes:
      - herdrx-data:/data
    security_opt:
      - no-new-privileges:true
volumes:
  herdrx-data:
```

公网部署必须有 HTTPS 反向代理，并正确设置安全 Cookie；提供单独的本地 HTTP 示例，明确要求将 `HERDRX_COOKIE_SECURE` 设为 `false`，避免用户用 HTTP 部署后无法登录。不能把 HTTP 作为公网默认教程。

### 2.2 服务器接入

先安装我们发布的对应平台二进制。发布时提供一个稳定安装入口和手动下载方式；具体域名、Release 地址在发布阶段确定，不在方案中虚构可运行的安装脚本 URL。

安装完成后，常规操作只有两步：

```sh
herdrx setup
herdrx connect
```

`setup` 是一次性向导：

1. 检测 Herdr 可执行文件、运行用户、版本和必要接口。
2. 未安装时停止，提示先按 [Herdr 官方安装文档](https://herdr.dev/docs/install/) 安装，再运行本命令；不擅自下载或更新 Herdr。
3. 已安装但未运行时，解释需要 Herdr 后台服务；允许用户明确选择托管，见第 7 节。
4. 安装并启动 herdrx 用户服务，检查开机启动、登出保活、配置目录权限及真实运行状态。
5. 成功后提示运行 `herdrx connect`；失败则显示具体修复命令，不打印“已准备就绪”。

`connect` 通过本地 IPC 请求后台服务创建一次性授权，确认临时接入端点启动成功后输出：

```text
主机：my-server
Herdr：可用
后台服务：运行中，已配置登出保活

请在网站中选择：添加主机 → Tailcat 内网穿透
粘贴以下连接字符串（10 分钟内有效，只能绑定一次）：

herdrx://v1/<连接数据>

配对成功后长期有效，重启或更新无需重新配对。
请勿分享这段连接字符串。
```

网页只需填写“主机名称（可选）”和“连接字符串”。连接时显示“校验 → 建立隧道 → 确认授权 → 检查 Herdr”，成功后自动打开工作台。主机名默认来自服务器，但允许修改。

服务器不需要预先知道网站的域名或 URL，也不需要能直接访问网站入口；双方需要能访问共同的 DERP 基础设施。浏览器仍需正常访问网站。

### 2.3 日常维护命令

| 计划命令 | 行为 |
|---|---|
| `herdrx setup` | 检查环境、安装后台服务；重复执行不重新生成身份 |
| `herdrx connect` | 未绑定时生成一次性连接串；已绑定时显示绑定信息，不静默覆盖 |
| `herdrx connect --renew` | 未完成绑定时作废旧授权、生成新串；不重置已有正式绑定 |
| `herdrx connect --refresh-endpoint` | 在明确迁移 DERP 后导出签名端点更新包，供原网站恢复已有绑定，不授权新网站 |
| `herdrx status` / `status --json` | 查询真实 daemon 状态、绑定、Herdr、版本、最近连接 |
| `herdrx doctor` | 分层诊断 Herdr、用户服务、权限、DNS、DERP、协议兼容 |
| `herdrx logs` | 查看脱敏日志，支持跟随和时间过滤 |
| `herdrx service start/stop/restart` | 管理接入服务，不停止 Herdr 的 pane |
| `herdrx update --check` | 检查更新和兼容范围，不改变运行状态 |
| `herdrx update` | 安全更新接入组件并恢复连接 |
| `herdrx update --version <版本>` | 安装指定可信版本；不兼容或降级须显式确认 |
| `herdrx rollback` | 回退上一兼容二进制，不恢复已撤销的授权 |
| `herdrx revoke` | 本机撤销绑定，关闭现有访问；再次接入需重新授权 |
| `herdrx uninstall` | 卸载本产品服务，默认保留身份和配置，不卸载 Herdr |
| `herdrx serve` | 前台运行 daemon，供调试或非 systemd 环境自行托管 |

`connect --plain` 仅向 stdout 输出连接串，其余提示写 stderr，便于复制。默认不长期打印连接串，也不写入 daemon 日志。

## 3. 现状核查：可以复用什么，必须改什么

这是对当前工作区代码的静态核查，不等同于已经完成真实跨网测试。

| 当前实现 | 已有价值 | 缺口与处理建议 |
|---|---|---|
| [`cmd/herdrx-agent/main.go`](../../cmd/herdrx-agent/main.go) | 已有 init/run/pair/install/status/unpair | 初始化依赖网页提供 `allow-node`、SSH 公钥、URL、setup ID；改为服务器独立初始化 |
| [`internal/agent/service.go`](../../internal/agent/service.go) | 已有 systemd 用户服务、LaunchAgent、原子复制 | 未检查 linger；服务 PATH 与交互式终端不一致；缺完整更新和运行诊断 |
| [`internal/agent/config.go`](../../internal/agent/config.go) | 私钥持久化、0600 文件、原子替换 | 当前模型只能保存一组预置对端；CLI 与 daemon 并发整文件写入有覆盖风险 |
| [`internal/agent/runtime.go`](../../internal/agent/runtime.go) | Tailcat 内嵌 Go 库、虚拟 TCP 22、受限 SSH | 缺长期监督状态机；当前就绪日志输出完整 Tailcat 地址，升级到含 PSK 地址后必须脱敏 |
| [`internal/agent/sshserver.go`](../../internal/agent/sshserver.go) | 限定 exec 和 Herdr socket，支持撤销后断连 | SSH 公钥验证没有检查 `Paired` 或配对过期；临时配对和正式能力没有权限隔离 |
| [`internal/httpapi/tailcat.go`](../../internal/httpapi/tailcat.go) | 按用户保存加密凭据、固定 SSH 主机公钥 | 先远端确认，再创建本地 host/credential；缺跨端幂等恢复；标记 setup 已用的错误被忽略 |
| [`internal/hostruntime/factory.go`](../../internal/hostruntime/factory.go) | 每主机复用 SSH/Tailcat 连接 | 持全局锁拨号；仅有限次初始重试；业务错误也可能触发整个连接失效 |
| [`internal/httpapi/push.go`](../../internal/httpapi/push.go) | 已有无浏览器时的后台状态查询 | 串行查询会被慢主机拖住，需要独立主机任务与并发限额 |
| [`deploy/compose.yml`](../../deploy/compose.yml)、[`deploy/Dockerfile`](../../deploy/Dockerfile) | 已有非 root、数据卷、健康检查、可选 TLS | Compose 仍要求源码构建；CLI 构建产物未作为正式下载产品分发；缺发布流水线 |

特别说明：目前预置公钥持有者在 `Paired=false` 时可能进入受限执行/转发通道；这不是“任意匿名用户已经可以登录”的结论，但说明配对 token 还没有成为实际能力边界。公开分发前应优先修复。

## 4. Tailcat 调研结论与版本策略

### 4.1 能力边界

Tailcat 将用户态 WireGuard、NAT 穿透和 DERP 封装为 Go 能力，可无 root、无 TUN 使用；直连失败时走中继。沿用库集成，不额外启动 `tailcat` 命令行进程。[官方项目说明](https://github.com/tailscale/tailcat)

因此 Docker 默认不需要 `--privileged`、`NET_ADMIN`、`/dev/net/tun`、宿主机 Docker socket，受控服务器也不需要暴露公网 SSH 端口。虚拟 TCP 22 只存在于 Tailcat 网络栈内。

但 Tailcat 的包装层仍被上游标注为早期实验性工具，历史信任模型偏向“自己连接自己”。我们做多用户 Web 服务，必须额外限制地址解析、资源消耗和接入权限，不能把上游当作完整的多租户安全产品。[官方安全说明](https://github.com/tailscale/tailcat/blob/v0.6.0/SECURITY.md)

### 4.2 当前依赖与刚发布的变化

当前项目固定 `tailcat v0.5.0`，参考仓库停留于 `476c217fa`。截至本次查询，最新 Release 为 **v0.6.0，2026-09-04 18:46 UTC 发布**。新版本包含默认预共享密钥（PSK）、连接初始化重试等变化，不能直接照旧调研材料判断全部行为。[v0.6.0 发布说明](https://github.com/tailscale/tailcat/releases/tag/v0.6.0)

推荐将 v0.6.0 作为新链路候选基线，先完成兼容验证，再修改依赖；不在本轮调研中升级。它同时提高了 Go 要求到 1.27.1，并更换了关联的 Tailscale/WireGuard 依赖，需要成组锁定与测试。[v0.6.0 go.mod](https://github.com/tailscale/tailcat/blob/v0.6.0/go.mod)

接口层面的重点：

- 长期服务必须恢复节点私钥、PSK、确定的 DERP 配置；只恢复 `Server.Key` 不够。
- `Server.TailcatAddr()` 在成功启动后取得，并包含完整区域信息；含 PSK 的地址也是秘密，不能当普通主机名打印。
- `AllowedClients` 为空表示允许所有客户端，不表示拒绝所有客户端。
- 当前检查的 v0.5/v0.6 API 有 `AddAllowedClient`，没有对应的删除接口；撤销不能只修改磁盘上的白名单。

这些行为见 [v0.6.0 Server 实现](https://github.com/tailscale/tailcat/blob/v0.6.0/tailcat.go)。建议在 `internal/tunnel` 做小型适配层并写契约测试，隔离上游变动。

关于保活，旧材料中对 `Ping` 的描述需要细化：v0.5 的调用仍会发出 meow，但首次确认后的内部等待状态不能证明“本次确实收到新回包”。它不适合作为持续在线的唯一依据。后续使用有真实响应的 `DiscoPing` 配合应用探针；其行为也随版本做测试，不依赖首次握手成功的缓存结果。

## 5. 连接架构与首次授权

### 5.1 长期链路

```text
浏览器
  │ HTTPS / WSS
  ▼
Docker：herdrx-server
  │ 每个绑定独立的 Tailcat 客户端身份 + SSH 客户端密钥
  │ 优先直连，必要时经 DERP 加密中继
  ▼
受控服务器：herdrx serve（后台服务）
  │ 受限 SSH / 固定的 Herdr API socket 与终端命令
  ▼
同一操作系统用户的 Herdr 后台服务
  └─ workspace → tab → pane → 实际终端/Agent 进程
```

保留当前终端收发、图像上传和结构化 API，不再引入“原版 TUI”入口，不恢复浏览器互斥接管模式。网络重连不改变现有多人/多窗口输入语义；跨用户共享主机仍不属于本期。

### 5.2 选择：临时配对端点与正式端点分开

有三种可选做法：

| 做法 | 评价 |
|---|---|
| 把永久私钥或永久口令直接放进连接串 | 最省实现，但泄露后长期有效，撤销和换网站难以管理，不推荐 |
| 共用一个 Tailcat 服务，临时加入配对客户端，之后删除 | 网络栈更少，但上游缺少完整动态移除能力，权限和撤销容易混在一起 |
| **短期专用配对端点，成功后转到正式端点** | 多一次自动握手，但临时凭据完全不进入正式通道，推荐 |

临时端点只在用户执行 `connect` 后存在，最多一个、有效期 10 分钟。它使用独立的 Tailcat 节点密钥、PSK、临时客户端白名单和授权 secret，只能进行配对协议，**没有 exec、PTY、图片写入、任意端口/Unix socket 转发能力**。

正式端点使用另外保存的长期身份，只允许正式网站客户端公钥。首次配对期间最多同时存在两个网络栈；成功或过期后关闭临时端点。未绑定且没有有效连接串时，daemon 仍存在，但不开放远程终端能力。

这样旧连接串的临时地址和私钥在到期后没有服务可连接，既不依赖上游实时删 peer，也不会把正式端点的 PSK 放进最初那段连接串。

### 5.3 连接串格式

建议外观为 `herdrx://v1/<base64url(JSON)>`，不必注册操作系统协议处理器；网页当作普通文本解析。

载荷包含：格式版本、稳定的 agent ID、临时 Tailcat 地址、临时 Tailcat 客户端私钥、固定的 Agent SSH 主机公钥、enrollment ID、256 位随机配对 secret、到期时间，以及主机名/OS/版本/协议范围等展示信息。

其中的私钥是**只被临时端点允许的短期客户端私钥**，不是 Agent 节点私钥、正式网站私钥或系统 SSH 私钥。配对 secret 用于临时 SSH 认证；认证后只能进入固定的配对子协议。

Base64 只是编码，不是加密。拿到有效连接串的人可以抢先绑定，所以必须按敏感凭据处理：

- 不放 URL query/fragment、浏览器持久存储或后台日志；通过带认证与 CSRF 防护的 POST body 导入。
- 限制输入大小（初始建议 16 KiB）、嵌套深度、字段长度、版本和 DERP 节点数量；不合法输入不执行 DNS 回退或 shell 命令。
- Web 从连接串固定 SSH 主机公钥，后续不静默接受主机密钥变化。名称、OS 等自报字段只作展示，不参与授权。
- Agent 用自己的持久化到期时间校验，不信任用户修改的 `exp`；限制同一用户导入频率、并发握手和总时长。
- 临时信息仅在有效期内按需持久化，目录 0700、文件 0600；到期或成功后清除。允许 daemon 短暂重启后继续完成尚未到期的配对。

### 5.4 幂等绑定，防止“远端成功、网页失败”

不要在收到连接串后直接远端消费 token，再尝试保存数据库。建议使用可恢复的两阶段协议：

1. **Web 先保存准备记录。** 生成本次 `request_id`、`controller_id` 和正式 Tailcat/SSH 客户端密钥；将私钥加密保存，状态为 `pending`，绑定当前登录用户。
2. **临时通道 prepare。** 使用连接串接入临时端点，固定 SSH 主机公钥，再提交正式客户端公钥和请求 ID。Agent 原子预留该 enrollment，只接受一个 controller；相同 ID 和相同公钥重复请求返回相同结果。
3. **返回正式端点。** Agent 持久化 `prepared` 记录，启动只允许该正式客户端的端点，返回正式地址、binding ID 和提交挑战。此时正式密钥也只有 `commit/status` 权限，没有终端权限。
4. **Web 保存后验证。** Web 将正式地址与 binding ID 持久化，再通过正式通道证明持有正式私钥，并发送 `commit`。
5. **Agent 激活。** 持久化 `active` 后关闭临时端点、销毁临时凭据。提交应答丢失时，正式通道可重复查询/提交，不能要求重新复制连接串。
6. **Web 完成。** 根据正式通道状态，在同一数据库事务中完成凭据、host 与 enrollment 状态更新；重新建立正式权限的连接，拉取 Herdr 快照。

补充规则：

- 临时连接绝不在绑定成功后直接升级为终端连接；`prepared` 连接同样不能绕过重新鉴权。
- 新 prepare 必须在 10 分钟有效期内；已 prepare 的同一请求允许有限的恢复窗口（建议额外 10 分钟），仍无终端权限。超时清理无主的 prepared 状态。
- Agent 已 active 时，恢复凭正式密钥，不再依赖过期连接串；Web 不能因 HTTP 超时删除 pending 私钥。
- 两端进程在任一步崩溃，都能根据持久记录重试或明确失败；不把两端数据库描述成一个跨网络原子事务。
- 同一 agent 首期只接受一个 controller 绑定；不允许两个网站同时静默抢占。换网站先在本机 `revoke`，再 `connect`。

### 5.5 本地状态与撤销

daemon 是授权状态的唯一写入者；`connect/revoke/status` 通过仅当前用户可访问的 Unix socket 操作，避免 CLI 与 daemon 同时覆盖配置。初始化和离线迁移也必须持有进程锁，并校验文件所有者、拒绝危险软链接。

关键身份/授权写入采用临时文件、文件同步、原子替换及父目录同步，启动时恢复未完成事务；不能把“原子 rename”直接等同于断电后的完整持久性保证。

Agent 保存：身份版本、agent ID、正式节点私钥/PSK/SSH 主机私钥、固定 DERP 配置、Herdr 路径和会话范围、binding 状态、授权 epoch、临时 enrollment。

Web 保存：owner ID、host ID、controller/binding ID、协议版本、加密正式客户端私钥、加密正式 Tailcat 地址、固定 SSH 主机公钥、配对进度、最近健康结果。含 PSK 的地址不再作为普通公开 host 字段返回前端。

撤销时先持久化授权 epoch，再立即关闭该 binding 的 SSH 会话和正式 Tailcat 端点；不能只等下一次握手。再次绑定时轮换通道密钥/PSK，稳定的 agent ID 可以保留。备份或二进制回滚不能把旧授权恢复成有效状态。

网页删除主机与远端撤销是不同操作：在线时先申请撤销再删除；离线时可移除本地记录，但必须标注“未确认远端撤销”，提示在服务器执行 `herdrx revoke`。不能对离线主机声称已撤销成功。

正式绑定允许操作终端，本质上能以该 OS 用户执行命令，不是针对终端内容的沙箱。受限 SSH 的作用是收窄接入协议和配对前能力，不代表授权后的终端用户无法读取该账号有权访问的文件。

## 6. 长期保活、重连与网络条件

### 6.1 三层职责

| 层次 | 负责什么 | 不负责什么 |
|---|---|---|
| 操作系统服务管理器 | 开机启动、进程退出后重启 | 不能证明远程链路和 Herdr API 正常 |
| Agent daemon | 维护 Tailcat 端点、身份、授权和 Herdr 可用性 | 不把浏览器在线与否当作退出条件 |
| Web HostRuntime | 每主机拨号、真实健康探测、断线重建、终端恢复 | 不因一个主机离线而阻塞其他主机 |

`herdrx` 的“后台常驻”是一种服务生命周期，不意味着必须维持永远不关闭的同一个 TCP 连接。连接可以重建，身份和绑定不能因此丢失。

### 6.2 Web 重连状态机

建议状态：`dormant → connecting → online_direct / online_relay → reconnecting`；另外单独保存 `herdr_unavailable / incompatible / revoked` 原因，不能全显示成“连接失败”。

实现要求：

- HostRuntime 由服务级 context 管理，不绑定某个 HTTP 请求或浏览器标签页的寿命；按 owner/host 复用连接。
- 初始 `Ping` 只用于启动握手。在线后组合 `DiscoPing` 与有应答的 Agent 应用探针；Herdr API 健康独立记录。
- 起始参数建议：传输探针 30 秒、单次上限 10 秒、连续两次失败进入重连；SSH/应用探针必须真正回包，不能对当前直接丢弃全局请求的实现假定已有 keepalive。
- 链路 EOF 可立即重连；重试采用带随机抖动的退避，约 1、2、4、8、15、30 秒封顶。网络故障持续重试；撤销、身份不匹配、协议不兼容则停止盲目重试并提示人工处理。
- 失败后关闭旧 SSH/Tailcat 客户端，以相同的正式客户端身份创建新对象；避免复用失效握手缓存。每主机 singleflight，拨号时不持有全局 map 锁。
- 全局并发拨号初始上限 4，后台监控也要有独立任务与并发上限。Herdr 返回“pane 不存在”等业务错误不能直接销毁整个网络连接。
- 重连后先恢复结构快照，再重新订阅需要的终端流；旧连接 epoch 的迟到帧丢弃。输入发送结果不确定时不得自动重放可能已执行的命令。

探针间隔是初始设计参数，需结合 DERP 配额、延迟、连接数量实测调整。不能只因为进程在或最近一次 Ping 成功，就显示“在线”。

### 6.3 多主机容量

当前“每主机一个 Tailcat Client”会带来独立用户态网络栈，不能把其成本当作普通 TCP 连接。先沿用 v0.4 的小团队定位，测试 10/50/100 个配置主机，不预先承诺测不到的容量。

第一版建议保留每实例默认 20 个持续在线热连接的保护上限，允许配置；正在打开的主机优先，超出上限的空闲主机进入 `dormant` 并显示“按需连接/上次在线”，不假称在线或离线。Agent 在全部服务器上仍持续运行，用户打开任一主机时自动使用已有绑定连接，不需再次粘贴字符串。

这个上限属于 Web 侧资源策略，不是 CLI 存活时间限制。P0 基准若证明可承受更多，再提高默认值；如目标为数百台全部持续监控，再评估共享网络栈或反向连接拓扑。后者会引入服务器发现网站地址的问题，本期不为此牺牲当前要求的独立连接串流程。

### 6.4 DERP 与稳定地址

公共 DERP 可让用户先快速接入，但上游说明它有速率限制；不能把它包装为本产品的无限带宽或 SLA。[Tailcat 官方说明](https://tailscale.com/tailcat)

推荐分层：

1. 默认提供开箱即用的公共 DERP 配置，并在诊断中检查可达性。
2. 生产部署支持替换为自建 DERP；如项目对外提供托管中继，需要单独确定费用、地域、配额、滥用处理和运维责任，不能在当前方案里默认已有这一基础设施。
3. 普通 Docker 用户不必先搭 DERP 才能试用；需要稳定生产中继时，由管理员统一配置，受控服务器可通过 `setup --derp-config <文件>` 选择同一基础设施。

正式身份必须固定接入所需的区域信息，不能每次重启随机选择一个新区域后仍要求旧地址可用。保存启动成功后的有效长地址和 DERP 配置，缓存必要元数据；在同一固定区域内配置多个节点可降低单节点故障风险。

**本期不承诺跨区域无感迁移。** 如果原 DERP 区域整体永久下线，而且没有预先协商的替代入口，仅靠旧连接串和心跳无法“找到”换了区域的 Agent。提供端点恢复流程：用户明确修改 DERP 配置后，运行 `herdrx connect --refresh-endpoint` 生成由既有 SSH 主机身份签名的更新包；包内绑定 agent/controller ID、递增端点版本及有效期。Web 仅允许该 host 的 owner 导入，验证签名、版本并持久化后确认；只更新端点，不新增 controller 或降低授权。更新包也含敏感地址，不得公开。若主机身份也丢失，则必须重新配对。

网络要求与限制：

- 两端需要访问所选 DERP（通常为出站 TLS/TCP）；允许 UDP 有利于 STUN 与 P2P，具体端口按配置验证。
- 没有公网入站端口、没有端口映射通常仍可使用中继；完全不能访问共同中继的隔离网络无法凭空穿透。
- Docker 默认 bridge 可能增加一层 NAT，必须以 bridge 模式实测；host network 只作为高级选项，不作为默认安装前提。
- 区分 DNS、DERP 不可达、UDP 不通、身份失败和 Herdr 不可用；展示中继不应被渲染成故障。
- 导入连接串会使服务端主动拨号，必须限制其中 DERP/DERPMap 的出站目标。默认拒绝 loopback、链路本地、云元数据地址及未批准的私网中继；私网 DERP 由实例管理员显式放行。检查解析结果、重定向与实际连接，防止 DNS rebinding。不要因此一刀切禁止已认证对端协商出的正常局域网 P2P 路径。

## 7. Herdr 检测与后台服务

### 7.1 检查安装，更要检查实际可用性

不能只检查 `which herdr`。按顺序验证：

1. 在当前 OS 用户下解析真实可执行文件绝对路径，支持 `--herdr-bin` 显式指定；记录运行时所需 PATH，不依赖 shell alias、交互式 `.zshrc` 或临时版本管理器环境。
2. 读取 `herdr --version`，检查可执行文件自身的 API schema，以及实际正在运行的服务状态。二进制升级后 daemon 可能仍是旧版本，二者要分别记录。
3. 针对本项目真实使用的快照、输入、终端输出和图片粘贴能力做白名单探测；版本号不能替代能力检查，探测不能顺便创建/关闭用户 pane。
4. 验证目标会话 API socket 存在、属于预期用户并可访问。默认只开放配置的会话；命名 session 必须显式选择，不接受远程提交任意绝对 socket 路径。

Herdr 官方提供 `herdr status server`、`herdr api schema --json` 和用于受监督运行的 `herdr server`，无需发明 `--daemon` 参数。[Herdr CLI 文档](https://herdr.dev/docs/cli-reference/)

### 7.2 接入组件与 Herdr 生命周期分离

Agent 升级时只重启 `herdrx.service`，Herdr 应在独立服务/已有后台进程中运行，不能成为随接入组件一同被 systemd 清理的子进程。

`setup` 的处理原则：

- Herdr 已运行：使用现有服务，不接管、不重启。
- 未运行：提示用户自行启动 Herdr。项目不接管其服务，CLI 不提供 `setup --manage-herdr`。
- 已有自定义 Herdr unit：不覆盖；报告冲突及需要的配置。
- 命名 session 的启动参数、XDG 环境、服务停止行为须在 Linux 集成测试中验证，不能根据默认会话推断全部适用。

关闭网页或重启接入组件，不应终止 Herdr 任务；**操作系统重启后，原进程不能继续存活**。可恢复的布局、历史和 Agent 会话取决于 Herdr 的恢复能力，不能承诺“机器重启后所有命令无中断继续执行”。[Herdr 持久化说明](https://herdr.dev/docs/persistence-remote/)

### 7.3 Linux 首发

建议使用 systemd 用户服务，固定执行入口 `herdrx serve`，`Restart=always`、适当 RestartSec/启动限流，网络异常在进程内部重试。不要依赖用户级 `network-online.target` 就认为外网已经就绪。

必须检查 `loginctl` 的 linger：启用后用户管理器才可以在开机时启动并在登出后继续保留。环境权限不够时，提示管理员执行针对该用户的 `loginctl enable-linger <用户名>`；在完成前明确显示“未保证登出/开机保活”。[systemd 官方 loginctl 文档源码](https://raw.githubusercontent.com/systemd/systemd/main/man/loginctl.xml)

默认以拥有 Herdr 的普通用户运行，不要求 root daemon。不要因为用户使用 `sudo` 安装就无意中把服务装进 root 的配置目录。对无 systemd、无 user manager 的环境提供前台模式及明确诊断，不虚报安装成功。

配置遵循 XDG，建议路径：

- `~/.config/herdrx/config.json`：配置、身份与授权状态；
- `~/.local/state/herdrx/`：运行状态、脱敏诊断；
- `$XDG_RUNTIME_DIR/herdrx/control.sock`：只允许当前用户访问的本地控制入口；
- `~/.local/share/herdrx/releases/<version>/herdrx`：版本化二进制；
- `~/.local/bin/herdrx`：稳定启动入口。

无法取得安全 runtime 目录时使用受限备用目录，并校验所有者/锁；不暴露无认证的本地 TCP 管理端口。

macOS 后续采用 launchd，但现有 LaunchAgent 路径只应承诺用户登录后的运行，不等同于无人登录时的开机服务。Windows 需要另做服务管理和 Herdr socket/进程适配，不把上游支持 Windows 等同于本项目已支持。

## 8. 更新、回滚与公开分发

### 8.1 CLI 更新事务

`herdrx update` 推荐执行：

1. 获取受信任发布源的版本清单，检查平台、架构、最低数据格式和服务端协议范围；支持 stable/preview，但默认 stable。
2. 校验清单签名、过期时间/防降级信息，再校验下载产物 SHA-256。仅下载 SHA 文件不能证明产物可信；建议内置 Ed25519 发布验证公钥，并设计签名密钥轮换。
3. 下载到同一文件系统的临时版本目录，检查磁盘空间、文件类型、长度和权限，执行不启动网络服务的 `self-test`。
4. 持有更新锁，保存兼容回退信息，原子切换稳定入口；不能先删除旧二进制。防止并发更新或更新中途断电造成入口缺失。
5. 通过原服务管理器重启 **herdrx**，等待本地 IPC 返回预期版本、状态格式可读、授权可加载。健康窗口建议 30 秒。
6. 本地就绪失败则回退到上一兼容二进制并重启；保留清晰的失败原因。互联网暂时不通不是自动回滚的充分理由。

配置和授权不在常规更新中重新生成。更新可能短暂中断网页连接，但不停止 Herdr 及其 pane；网页自动恢复。不能把它描述成完全零中断升级。

回滚仅在声明兼容的版本间进行，不盲目恢复旧配置备份，不复活旧 token、旧 controller 或已撤销 epoch。数据格式迁移需向后兼容一个明确窗口；不兼容降级应拒绝并说明原因。

默认每天最多检查一次版本（可关闭），不自动应用；可显示可升级状态。自动更新后续仅作为显式启用的策略，带维护时间窗和版本范围。网页第一版只展示状态/更新命令，不开放任意远程脚本执行能力。

### 8.2 Docker 更新与数据

用户选择新镜像版本、备份数据后执行：

```sh
docker compose pull
docker compose up -d
```

`pull` 获取新镜像，`up -d` 负责重建容器；单独 `restart` 不会把旧容器替换成新镜像。[Docker pull](https://docs.docker.com/reference/cli/docker/compose/pull/)、[Docker up](https://docs.docker.com/reference/cli/docker/compose/up/)

必须持久化 `/data` 内的数据库、加密主密钥、网站实例身份及全部绑定凭据；仅备份数据库、不备份解密密钥，无法恢复连接。提供一致性备份方式，包含 SQLite WAL 处理；容器替换不会自动删除具名卷，但教程不得使用 `down -v` 作为普通更新步骤。[Docker volumes](https://docs.docker.com/engine/storage/volumes/)

`restart: unless-stopped` 用于容器异常退出及 Docker 重启后的恢复。Docker 的健康检查不等于“unhealthy 自动重启”；进程遇到不可恢复故障应正确退出，持续外网故障则继续运行并暴露诊断。[Docker 重启策略](https://docs.docker.com/engine/containers/start-containers-automatically/)

本期仍为单实例，不让两个容器同时挂同一数据库和 Tailcat 身份作 HA。网站的数据卷被复制到另一台机器时，应有明确的迁移流程，不能同时使用相同客户端身份运行两套控制端。

### 8.3 发布流水线

一个版本统一产出：

- Linux amd64/arm64 CLI 压缩包、校验和、签名版本清单；
- 多架构网站镜像、不可变版本标签、镜像摘要；
- 不含源码构建步骤的 Compose、环境变量示例、安装脚本和手动安装说明；
- Server/CLI/Tailcat/Herdr 能力兼容表、升级与回滚说明、依赖许可清单和 SBOM。

CI 顺序为单元测试、Go race/静态检查、前端测试/构建、协议集成测试、镜像启动和升级测试，再发布。可附 GitHub 构建来源证明；它与 CLI 内置签名校验互补，不能让用户必须安装 GitHub CLI 才能更新。[GitHub artifact attestations](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations)

容器内只需要 `herdrx-server`，管理远端主机无需安装 Herdr。网页的“本机”连接在容器内指容器自身，不是 Docker 宿主机：默认隐藏或解释这一入口；连接宿主机也走 CLI/Tailcat 或显式 SSH，不自动挂载用户 Herdr socket。

## 9. 代码落地边界与兼容迁移

| 模块 | 计划改动 |
|---|---|
| `cmd/herdrx` | 改为公共 CLI，统一 setup/connect/service/update/status/doctor |
| `cmd/herdrx-server` | 从当前 Web 服务端入口迁入，保留健康检查等容器功能 |
| `cmd/herdrx-agent` | 兼容旧名称一个迁移周期，明确提示新入口，不长期维持两套实现 |
| `internal/agent` | daemon、IPC、Herdr preflight、授权状态机、临时与正式 SSH 能力分离 |
| `internal/tunnel`（新增） | 封装 Tailcat 版本差异、身份、探针、受控地址解析 |
| `internal/updater`（新增） | 签名清单、下载验证、原子切换、服务恢复与兼容回滚 |
| `internal/httpapi/tailcat.go` | 导入连接串、异步绑定进度、幂等恢复、状态与撤销接口 |
| `internal/store` | enrollment/binding/协议字段、唯一约束和事务、秘密地址加密 |
| `internal/hostruntime`、后台通知 | 独立重连、限流与健康分层，移除持全局锁拨号 |
| `web` 主机表单与设置 | Tailcat 内网穿透类型、粘贴输入、进度/错误、直连/中继/版本状态 |
| `deploy`、Makefile、CI | 公共镜像、CLI Release、双架构产物、安装和更新测试 |

建议 API：`POST /api/tailcat/enrollments` 创建/恢复导入任务，`GET /api/tailcat/enrollments/{id}` 查询进度；读取和修改都校验 owner。任务脱离浏览器请求短期运行，有明确超时和恢复状态；页面刷新不重复创建主机。

迁移要求：

1. 保留现有 `~/.config/herdrx-agent/config.json` 的身份和已确认绑定，按状态迁移，不能直接当新安装覆盖。`Paired=false` 不能未经确认转换成正式授权。
2. 停止旧名称的服务、迁移入口、启动新服务，保证同一 OS 用户只有一个 daemon；迁移失败保留旧文件且可恢复，不同时运行两套相同节点身份。
3. 现有部署脚本、Docker ENTRYPOINT、Makefile、健康检查一起更新，避免把 CLI 误当网站启动。
4. v0.5 无 PSK 的正式通道不能直接切到 v0.6 自动生成 PSK，否则网站保存的旧地址立即失效。先升级 Web 到兼容版本；旧绑定暂走显式 legacy 兼容路径，之后经已认证通道交换新端点、确认持久化再切换。新配对不允许降级为无 PSK。
5. 协议迁移必须测试“旧 Web/新 CLI、新 Web/旧 CLI、双方新版本”。不兼容时给出升级顺序，不删除 host、不让用户只能看到泛化超时。
6. 旧配对入口先加正式能力门禁，再逐步下线。新旧流程同时存在时也不能绕过 single-controller 与 owner 约束。

上述只改 herdrx 产品仓库；`../herdr`、`../tailcat`、`../orca`、`../paseo` 均为只读参考。

## 10. 实施顺序与完成闸门

按可验收的工作包推进，不把“编译成功”当作成品完成。

| 阶段 | 交付内容 | 必须通过的闸门 |
|---|---|---|
| P0：风险验证 | v0.6 适配小样、临时/正式端点、固定身份重启、真实 Docker 网络验证 | 临时凭据不能操作 Herdr；节点和 PSK 恢复后可连；prepare/commit 丢应答可恢复；测出网络栈基础成本 |
| P1：CLI 与服务 | 公共命名、setup/preflight、IPC、服务安装、status/doctor | 新 Linux 机器完整安装；未装 Herdr 明确退出；登出与开机保活；不影响已有 Herdr |
| P2：一次粘贴闭环 | 连接串、绑定状态机、Web 表单、owner 隔离、撤销 | Docker 网站 → 新服务器 CLI → 粘贴 → 终端输入/图片；重复导入不重复建主机 |
| P3：长期可靠性 | HostRuntime 重构、探针、重连、容量保护、DERP 诊断 | 跨网直连/中继、断网恢复、空闲 24 小时、慢主机不拖其他主机 |
| P4：更新与分发 | 签名更新/回滚、多架构镜像与二进制、迁移、用户文档 | 普通用户无需源码启动；升级保留绑定；坏包回滚；Herdr pane 不被误停 |
| P5：发布验收 | 真实多服务器与 72 小时 soak、兼容矩阵、已知限制 | 完成下面的故障矩阵，才能标记“可公开长期使用” |

P0 只使用临时数据目录/测试会话和隔离端口，不升级当前在用服务，不用用户现有 pane 做断网、崩溃或升级实验。实现阶段开始前再安排具体改动和运行服务切换。

### 验收矩阵

| 场景 | 预期结果 |
|---|---|
| 服务器没有 Herdr / PATH 找不到 / daemon 未运行 | 分别定位并给出指引；不生成看似可用的终端授权 |
| 新安装后退出 SSH 登录，再重启系统 | herdrx 按服务配置启动，绑定保留；Herdr 的恢复能力单独验证 |
| 杀死接入 daemon / 暂时断网 / DNS 短暂故障 | 自动恢复，不重新配对、不关闭已有 Herdr pane |
| 重建网站容器并保留数据卷 | 主机和密钥不变，自动恢复连接 |
| 关闭全部浏览器 24 小时 | Agent 不退出；热连接/按需连接符合策略；再次打开无需连接串 |
| 不同 NAT 的两台机器，Docker 使用 bridge | 能识别真实路径；UDP 被阻止时中继可用，恢复 UDP 后重新尝试直连 |
| 一个主机拨号超时，其余正常 | 其他主机仍可操作，重试并发/CPU/FD 有界 |
| 连接串过期、篡改、同串并发导入 | Agent 拒绝越权；至多一个绑定成功；错误不会泄露 secret |
| 有效临时凭据尝试 exec、PTY、转发、图片上传 | 全部拒绝；只有配对子协议可用 |
| prepare、commit 前后分别丢应答/重启两端 | 可恢复同一请求，无重复 host，无已授权却丢失正式私钥的静默坏状态 |
| 本机 revoke 时终端正在连接 | 当前访问被关闭，旧正式和临时凭据均无法重新控制 |
| 导入畸形地址、超大载荷、私网/元数据 DERP 地址 | 无 panic、无任意出站访问、资源占用有上限 |
| 更新包签名错、损坏、下载中断、空间不足 | 旧版本继续可用，不破坏稳定入口 |
| 新 CLI 启动失败 / 离线环境 / 不兼容回滚 | 按本地就绪和兼容规则处理，不误恢复旧授权、不因 WAN 故障盲目回滚 |
| CLI 更新时 Agent 正在工作 | 网页短暂重连，Herdr 与 pane PID 不变，任务不中断 |
| 旧绑定迁移到含 PSK 地址 | 两端确认新地址后再切换；异常可恢复，身份不被无声重置 |
| 终端输入、Shift+Enter、滚动、分屏、图片粘贴 | 既有功能回归通过，不因接入重构退化 |
| 连续运行 72 小时，配置 10/50/100 台分档测试 | 记录 RSS、CPU、FD、goroutine、重连、延迟与中继流量，据实确定容量 |

跨网与系统重启项目必须在真实可控的测试服务器执行；同机 loopback、单元测试或浏览器假网络不能代替。本文尚未执行这些新增验收项。

## 11. 需要评审的选择与我的意见

技术方向已经足够明确，以下可以按推荐值推进设计，不需要用户决定密钥算法或内部 API：

1. **连接串默认 10 分钟一次性，绑定长期有效。** 不建议提供默认永久万能连接串；如果以后需要批量无人值守部署，另做可限制次数/用途的 enrollment token。
2. **Linux 两个主流架构先交付完整服务体验。** macOS 后续，Windows 单独立项；这是本项目适配范围，不是否认 Herdr 的平台能力。
3. **默认手动一键更新，通知可关闭。** 自动更新必须显式启用；不把 Herdr 本体升级塞进 `herdrx update`。
4. **公共 DERP 用于默认接入，支持私有 DERP 用于生产。** 若要对外承诺中继可用性或提供项目托管中继，需要另外确认基础设施预算和运营责任，这是发布前真正需要产品方决定的事项。
5. **单网站绑定、单 owner、可信实例。** 首期不引入跨网站多 controller、共享主机权限或多副本服务；保留以后扩展的数据结构。

最终交付标准不是“有一个常驻进程”，而是：**用户能用 Docker 启动网站，在服务器执行两条命令、复制一次字符串，然后长期连接；遇到断网、重启、更新能恢复，遇到授权问题能解释和撤销。**
