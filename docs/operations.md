# herdrx 运维说明

网站采用可信管理员、单实例 Docker 部署。SQLite、WebSocket 状态和 Tailcat 客户端在同一进程，不能多个实例并发使用同一数据目录。

## 启动与检查

部署文件、目录权限与 ZOT 登录见 [快速开始](../README.md#快速开始)，镜像构建见 [发布流程](deployment.md)。在部署目录执行：

```bash
docker compose pull
docker compose up -d --wait
docker compose ps
docker compose exec herdrx /app/herdrx-server healthcheck
```

首次初始化 token 位于本地 `./data/bootstrap-token`，日志仅提示文件位置。管理员创建成功后文件删除，令牌原文不写入日志。

## HTTPS

herdrx 默认只监听 HTTP，使用 IP 地址即可部署和测试，不附带 HTTPS 代理或证书管理。`HERDRX_PUBLIC_URL` 填实际访问地址，HTTP 使用 `HERDRX_COOKIE_SECURE=false`。

部署者需要 HTTPS 时，在自己的反向代理中配置证书，并将请求转发到网站 HTTP 端口。代理需要支持 WebSocket。应用侧配置示例：

```dotenv
HERDRX_PUBLIC_URL=https://herdrx.example.com
HERDRX_COOKIE_SECURE=true
# 仅适用于反向代理在同一宿主机运行、通过本机端口访问的情况。
HERDRX_BIND_ADDR=127.0.0.1
```

若代理运行在其他机器或容器中，按实际网络拓扑设置监听地址和后端访问规则。`HERDRX_TRUSTED_PROXIES` 仅填写该代理的实际出口 IP/CIDR；直连模式保持为空。反向代理的安装、证书和数据由部署者自行管理。

PWA 安装、Service Worker 和 Web Push 受浏览器安全上下文限制：本机 localhost 可用于相应回归，普通 HTTP/IP 下保留网站和终端访问，并按可用能力显示安装或通知入口。真实设备安装属于部署环境的单独验证记录，不要求本机 HTTP 测试先准备域名。

## 数据与备份

所有路径均相对部署目录，网站 `./data` 至少包含：

- `herdrx.db` 及可能存在的 WAL/SHM 文件；
- `master.key`：解密 SSH/Tailcat 凭据所需；
- `vapid.json`：Web Push 身份；
- 首次初始化前的 `bootstrap-token`。

数据库和密钥必须一起备份。缺少 `master.key` 无法恢复加密凭据。停机备份示例：

```bash
install -d -m 700 backups
backup_file="backups/herdrx-$(date +%Y%m%d-%H%M%S).tar.gz"
docker compose stop herdrx
sudo tar -czf "$backup_file" data .env
sudo chmod 600 "$backup_file"
docker compose up -d --wait
```

先确认归档成功，再将备份保存在另一处受限存储；归档失败时修正备份问题并重新启动服务。备份会包含配置和密钥，不要提交 Git。在线备份需使用 SQLite backup/VACUUM INTO，不能只复制正在变化的主 DB 文件。

恢复时停止网站，在干净部署目录解压备份，保留文件属主，核对 `.env` 与镜像兼容性后启动。不要把网站身份与受控端身份混为一份备份：受控端的配置、PSK、binding 和 epoch 需单独保存，恢复旧身份不能复活已撤销的访问授权。

## 目录权限与原生实例迁移

镜像使用 `65532:65532`。首次 `sudo bash prepare-data.sh` 初始化 `./data`，不会递归修改已有非空目录；这类目录属主不同会直接报错。

从原生网站迁移时，先停止旧实例并做一致性备份，再将完整数据复制到新部署目录。只对已确认的目标副本执行：

```bash
sudo chown -R 65532:65532 ./data
sudo chmod 700 ./data
```

确认旧实例停止后才启动容器，保留原备份用于恢复。不要同时启动两个网站共享数据库或 Tailcat 身份。不得用 `chmod 777` 或命名卷绕过权限问题。

## 自建 DERP

可选的 `compose.derp.yml` 从源码构建独立 DERP 镜像，未包含在网站发布流水线。它需要独占公网 TCP 80/443 和 UDP 3478，不能与同一 IP 上的其他服务共用这些端口；证书 bind 到 `./derp/certs`。

新版接入在受控端执行 `herdrx setup --derp-config ./derp.json` 选择中继；配置文件格式、迁移与签名端点更新见 [Tailcat 教程](tailcat-quickstart.md#自建中继与端点迁移)。网站通过连接串读取同一区域，不需要另外修改网站域名或启用 HTTPS。`HERDRX_DERP_HOST` / `HERDRX_DERP_REGION_ID` 只供旧版初始化兼容入口使用。

`herdrx doctor --network` 对保存的中继配置进行 DNS/地址校验和实际 DERP 协议握手，单节点最多等待 5 秒，最多 4 个并发探测。它与 Herdr、守护进程和绑定状态分开报告，不以中继握手成功推断终端是否走直连。公共中继的服务条件由上游决定，本项目不提供中继 SLA。

## 受控端与访问边界

按网站三步引导完成 CLI 安装、用户服务保活和最后绑定，完整命令见 [Tailcat 接入教程](tailcat-quickstart.md)。以 Herdr 用户执行 `herdrx setup && herdrx status`，确认 linger 后用 `herdrx connect --plain` 生成一次性绑定凭据。诊断用 status/doctor/logs，撤销用 `herdrx unpair`。删除网页主机记录不等于撤销远端绑定。

下载信息由网站后端查询固定 GitHub 仓库的公开 Release 元数据，无用户凭据外传；可用结果缓存 5 分钟，失败或尚未发布缓存 1 分钟。网站到 GitHub 不通时，接入页提供 Releases 链接和重试，已有 CLI 仍可继续绑定；主机列表与已有连接不依赖这次查询。CLI 发布流程见 [维护者说明](cli-release.md)。

实例管理员可以读取终端明文并解密主机凭据；主机和凭据按用户归属隔离。SSH host key 变化会阻断连接，需要重新核验。网站和受控端的升级不应影响 Herdr 会话，恢复、跨网及长期运行证据按发版清单分别记录。

全屏应用滚动出现尺寸读取或协议错误时，先检查[滚动兼容要求](install.md#终端滚动兼容要求)：基础 SSH 的 Python 3、同用户 PTY 访问、Tailcat CLI 配套版本和 Unix socket 转发。已有原生控制客户端占用时不会被工作台抢占，释放控制后可重试。不要为恢复网页滚动重建 pane 或重启 Herdr。

## 登录与访问管理

系统只有管理员和普通用户两种角色。只有数据库尚无用户时才能持初始化令牌创建首位管理员；网站重启不重新开放初始化。普通用户只使用自己添加的 SSH/Tailcat，管理员也不能通过主机接口绕过归属访问其他用户的主机。本机 Herdr 只允许有效管理员使用，历史记录和已有连接同样复核权限。

注册默认关闭。管理员从主机页「管理 → 用户注册」手动开启邀请注册，然后在「邀请」栏目生成邀请码；新账号固定为普通用户。关闭注册不影响已有用户登录，尚未提交成功的注册请求会被拒绝。未过期且未使用的邀请码在重新开启后仍可用，不需要时单独撤销。开关和审计一起持久化，网站重启保留设置；旧 `HERDRX_REGISTRATION` 环境变量不再生效。多人同时管理时，过期页面的修改会被拒绝，刷新后重试。

「用户」栏目支持按邮箱/显示名称搜索，并按角色、账号状态筛选。禁用账号会在同一事务中删除全部 Web 登录及通知订阅，并断开该账号的访问；启用账号不会恢复旧登录或通知，需要重新登录并主动订阅。不能禁用自己或最后一位可用管理员。

「注销此登录」只影响选中的 Web 登录；「注销全部登录」影响该用户所有浏览器登录。两者均不会停止远程 Herdr、Shell、Agent 或任务，也不会撤销远程主机绑定。用户之前主动启用的后台通知可以在浏览器退出后继续工作；禁用账号会将其关闭。

实例正常注销或禁用立即撤销访问。登录期限到达时关闭 WebSocket；直接修改数据库导致的失效会在下一条操作前检查，空闲连接和输出还会定期复核（最长 5 秒）。已经提交给远程主机的操作无法撤回，重连不会重放输入。

所有写接口及 WebSocket 都校验来源，必须使用 `HERDRX_PUBLIC_URL` 或显式 `HERDRX_ALLOWED_ORIGINS`。脚本调用写接口也要携带正确 `Origin`；登录、注册和初始化仅接受 `application/json`。遇到 `origin_forbidden` 先检查地址配置，不要关闭校验。登录限流使用客户端 IP 和 IP/邮箱组合；非可信直连请求提供的转发头会被忽略。

使用已有反向代理时，只将其实际出口 IP/CIDR 加入 `HERDRX_TRUSTED_PROXIES`，限制后端端口的可达范围，并确认代理覆盖或正确追加客户端来源。不应填所有地址或整个内网网段。若公网入口前还有 CDN，需按该代理的文档单独配置可信 CDN，再以真实多客户端请求检查审计中的来源 IP。

## 共享 SSH 密钥与文件夹

「密钥」保存当前账号的可复用 SSH 认证材料，私钥和私钥口令继续使用现有主密钥加密，备份应同时包含数据库和主密钥。删除一台主机会保留共享密钥；仍被引用的密钥不能删除。替换密钥或 SSH 连接配置后需重新打开相关主机，只断开网站访问连接，远程 Herdr 与任务继续运行。

文件夹支持调整上级和移动主机；删除文件夹将直接包含的主机及子文件夹移到上一级，不修改凭据或终端。API、界面使用与 schema 3 升级说明见 [SSH 密钥与主机文件夹](ssh-keys-and-folders.md)。

## 多主机连接与故障恢复

同一远程主机的网页、终端和通知共用访问连接，同时进入多个页面只触发一次拨号。默认全实例最多 20 台已连接或待连接主机、4 个并发拨号；该值是保护上限，配置主机数量可以更多，实际承载能力需按工作负载测量。调整 `.env` 的 `HERDRX_MAX_HOST_CONNECTIONS` 和 `HERDRX_HOST_DIAL_CONCURRENCY` 后重建网站即可生效。

最后一个使用者离开后保留连接 2 分钟供重连复用；达到上限时优先回收最久未使用的连接。全部连接正在使用时，界面提示关闭暂不用的工作台或联系管理员调整上限。拨号默认 30 秒超时，网络故障按 2–30 秒退避；SSH 密钥未信任、身份变化或认证失败会停止自动拨号，核对并更新主机配置后重新连接。

工作台每 2 秒读取真实 Herdr 快照，单次最长等待 10 秒。连续 3 次失败后断开该访问连接并重新建立，恢复快照后回到可用状态。这里反映的是 Herdr 应答，不推测 Tailcat 采用直连还是中继。通知采用独立的 4 个采集任务和 4 个发送任务，失败主机按 10–60 秒退避，删除的主机和 pane 状态会清理。网站断开访问、回收连接和重连都不会关闭远程 Herdr 会话或重放输入。

## CLI 发行与更新

网站不会自动升级远程 CLI。远程主机可执行 `herdrx update --check` 查看已验签的最新正式版本，再用 `herdrx update` 安装；`herdrx rollback` 恢复兼容程序。签名信任根、离线更新、事务恢复和自定义路径见 [更新与恢复](update-and-recovery.md#cli-更新)。

## 通知服务出站访问

Web Push 订阅仅接受 HTTPS 443 的公开端点和有效浏览器密钥。通知发送在实际拨号时检查全部 DNS 地址，拒绝内网、回环、元数据及保留地址，使用已经检查的 IP 建连并保留 TLS 主机名验证；不跟随重定向。通知服务需要工作台能够直连推送供应商的公网 HTTPS，不继承环境变量中的 HTTP 代理。网站入口仍默认 HTTP，二者是不同的连接方向。
