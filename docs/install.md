# 安装 herdrx

网站镜像位于公开的 [Docker Hub：riba2534/herdrx](https://hub.docker.com/r/riba2534/herdrx)，支持 Linux amd64 / arm64；远程接入 CLI 名称为 `herdrx`。

## 网站

推荐按 [README 快速开始](../README.md#快速开始) 使用一条 `docker run` 命令部署，无需下载源码或登录镜像仓库。首次启动前为部署目录下的 `./data` 设置 `65532:65532` 属主和 `700` 权限，再通过 bind mount 挂载到容器的 `/data`。

`HERDRX_PUBLIC_URL` 填浏览器实际访问的地址：本机可用 `http://localhost:8080`，其他设备访问时改成工作台主机的 IP 和端口。默认提供 HTTP，无需域名；HTTPS 由自己的反向代理提供，详见 [运维](operations.md#https)。

需要沿用 Compose 时，可使用 [deploy/compose.yml](../deploy/compose.yml)、[配置示例](../deploy/.env.example) 和 [目录初始化脚本](../deploy/prepare-data.sh)，保存到同一部署目录，将 `.env.example` 复制为 `.env` 并设置访问地址后执行：

```bash
sudo bash prepare-data.sh &&
docker compose pull &&
docker compose up -d --wait
```

两种部署方式都使用该部署目录下的 `./data`，不能同时启动并共享数据。已有部署切换方式时先停止原网站，保留完整数据、访问地址和其他配置。

## 受控主机

先安装并独立运行 Herdr，以运行 Herdr 的同一用户操作。网站「添加主机 → Tailcat 内网穿透」提供完整的三步引导：

1. 复制页面的一行安装命令，从 [GitHub Releases](https://github.com/riba2534/herdrx/releases) 安装 `herdrx`；自动识别 Linux x86_64 / ARM64 并做 SHA-256 校验。
2. 执行 `~/.local/bin/herdrx setup && ~/.local/bin/herdrx status`，确认用户后台服务就绪，再检查 `loginctl show-user "$(id -un)" --property=Linger`；需为 `Linger=yes`。未启用时执行 `loginctl enable-linger "$(id -un)"`，权限不足再加 sudo。
3. 执行 `~/.local/bin/herdrx connect --plain`，在页面最后一步填写一次性绑定凭据并打开主机。

所有复制命令、手动安装、PATH 设置、无 systemd 环境、升级及排障见 [Tailcat 接入教程](tailcat-quickstart.md)。网页的一行命令直接使用 GitHub Release 安装脚本，并将下载与安装固定到同一版本，只有预发布时也可直接安装。安装过程不依赖工作台或个人域名；后续命令直接使用安装路径，无需设置 PATH。

已有自定义 unit 时 setup 拒绝覆盖；旧配置损坏或旧服务仍在运行时，迁移明确失败，不会创建替代身份。Linux amd64/arm64 的原生 systemd 生命周期、升级回滚和旧服务迁移已通过隔离 guest 验收；macOS 受控端完整服务支持不在首发范围。详见 [当前验收](release-validation-2026-09-07.md)。

## 工作台自带中继

CLI v0.1.0-rc.2 起，网页连接命令可携带 20 分钟有效的中继授权。网站仅在 `HERDRX_PUBLIC_URL` 为公网 HTTPS、现有反向代理支持 `/derp` 协议升级且实际握手成功时提供此命令。CLI 从远程主机再次探测，失败时在接入前回退公共中继；所选区域随凭据固定，双方使用同一区域。默认 HTTP 安装保持可用，不负责申请证书。可选 STUN 与代理要求见 [运维](operations.md#工作台自带中继)。

已有 CLI 先升级，再从主机卡片「更新连接端点」复制命令并导入签名更新包；无需解除绑定。已绑定区域不会在运行期间自动切换到另一个区域。

## 终端滚动兼容要求

普通历史由 Herdr 快照提供；全屏应用通过 Herdr 原生滚动通道接收鼠标或手指手势。为避免接入时改变终端网格或像素尺寸，这条通道要求：

- Herdr 所在主机为 Linux，连接用户与 Herdr 用户相同，能读取 pane 进程的 `/proc/<pid>/fd/0` 终端尺寸。
- 基础 SSH 主机已安装 Python 3，并允许 Herdr API 与 client 两个 Unix socket 转发。
- Tailcat 使用与本次网站配套构建的 herdrx CLI，提供只读尺寸命令和 client socket 转发；不需要 Python。已有 CLI 更新后需按当前服务配置重启 herdrx 访问服务，Herdr 和任务继续运行。
- 适配的 Herdr 私有协议为 20 和 22；20 已在 Herdr 0.8.2 上运行验证，22 使用对应源码的独立协议样例测试。其他版本及不能准确还原的像素尺寸会明确报错，不尝试猜测输入协议或改变尺寸。

实体手机、macOS 远程尺寸读取及真实跨网手感的验收边界见[滚动与手机手势验收](flicker-mobile-validation-2026-09-06.md)。

## 日常命令

| 命令 | 作用 |
| --- | --- |
| `herdrx status` | 查询 daemon 状态 |
| `herdrx doctor` | 分层诊断 |
| `herdrx logs` | 读取用户服务日志 |
| `herdrx service stop/start/restart` | 管理 herdrx 服务 |
| `herdrx unpair` | 撤销绑定、断开访问；重新接入用 `herdrx connect` |
| `herdrx update --check` / `herdrx update` | 验证最新正式版本 / 签名更新并核验本地服务；预发布使用 `--version` |
| `herdrx rollback` | 切回兼容程序并核验；身份与撤销保持，详见恢复文档 |

当前没有 `herdrx revoke` 命令；撤销使用 `unpair`。停止 herdrx 不会停止用户的 Herdr 或 pane。

## 登录和首次升级

首位管理员在初始化页面创建，令牌来自网站数据目录的 `bootstrap-token` 文件（镜像部署由宿主机读取 `./data/bootstrap-token`）。若明确通过 `HERDRX_BOOTSTRAP_TOKEN` 配置，则使用配置值。令牌原文不再写入服务日志。

新实例和从旧版首次升级的实例均默认关闭注册。创建首位管理员后，注册仍保持关闭；网站重启不会再次允许初始化。管理员在「管理 → 用户注册」手动开启邀请注册，再在「邀请」栏目生成一次性邀请码。注册开关保存在数据库中，旧 `HERDRX_REGISTRATION` 环境变量不再生效。

关闭注册会隐藏注册入口并禁止创建邀请，已有用户可以继续登录。邀请码原文只在创建时显示，不进入列表、日志或审计详情。受邀账号固定为普通用户，只能访问自己添加的 SSH/Tailcat 主机；本机 Herdr 只允许管理员添加和使用。Docker 内的本机模式指网站容器环境，不能直接替代宿主机接入。

HTTPS 由部署者自己的反向代理提供，应用配置见 [运维](operations.md)。从旧版升级前备份整个数据目录，并阅读 [访问管理迁移规则](update-and-recovery.md#访问管理迁移与旧版回退)。

## System OpenSSH（Kerberos）

管理员可设置 `HERDRX_SSH_BIN=/usr/bin/ssh`，在「添加主机 → SSH → 认证」选择「System OpenSSH」。主机名可填 SSH alias，用户名留空、端口填 `0` 时使用服务账号的 SSH 配置；显式值覆盖配置。该模式不读取网站保存的密码或私钥，不允许普通用户使用工作台的系统身份。

先以**运行网站的同一操作系统账号、同一环境**执行 `klist -s` 与 `ssh -T -o BatchMode=yes -o StrictHostKeyChecking=yes devbox true`；没有 ticket 时由管理员运行 `kinit`。首次连接先通过可信渠道核对远端指纹并配置该账号的 `known_hosts`。网站严格检查 host key，不在网页接受系统 SSH 指纹，也不弹出密码提示。SSH 配置示例：

```sshconfig
Host devbox
    HostName devbox.example.com
    User developer
    GSSAPIAuthentication yes
    # 如需跳板，可设置 ProxyJump jump.example.com
```

已有主机不会自动切换认证：请在原主机卡片「连接设置」中选择 System OpenSSH，并将地址改为已验证的 SSH alias；保存后重新打开，原 Herdr 会话保持。使用 launchd/systemd 托管网站时，应在服务环境中显式配置与成功测试一致的 `KRB5CCNAME`（若使用文件缓存）及所需 Kerberos 环境；后台服务不会自动继承终端的环境变量。

OpenSSH 原生读取 SSH 配置与 `KRB5CCNAME` 等运行环境，因此支持系统 GSSAPI 及跳板配置。远程仍需已独立运行 Herdr，允许 API/client Unix socket 转发；观察、控制、图片和滚动沿用现有 Herdr 协议。配置文件中的命令（例如 ProxyCommand）属于管理员可信配置，不接受浏览器传入任意 SSH 参数或可执行文件。

默认 distroless 镜像**没有 OpenSSH 或 Kerberos 运行库**。仅设置变量或挂载 Mac 的 SSH 文件不能让 Linux 容器使用 macOS 的系统 ticket。Mac 上优先让网站以持有 ticket 的用户原生运行；需要 Docker 时应自行提供含 OpenSSH/GSSAPI 的 Linux 运行环境、该 Linux 用户可访问的票据缓存、SSH 配置及 known_hosts，并先在容器内用相同用户验证上述命令。不要挂载整个宿主机 home、把 keytab/ticket 放进镜像，或将通用远程命令执行服务暴露给容器。

## SSH 密钥与分组

Herdr 必须已由该用户独立安装并运行。终端观察命令先使用非交互 SSH 的 `PATH`，再查找 `~/.local/bin` 和 `/usr/local/bin`，不依赖交互式 Shell 的初始化文件。安装在其他目录时，请配置远程非交互 SSH 的 `PATH`。终端观察进程退出会显示原因；修复远程环境后点击「重连终端」，不会重新创建 pane 或重放关闭期间的输入。

在「添加主机 → SSH」填写地址、自定义端口和 SSH 用户，选择密码认证或已保存密钥。已有密钥先通过主机页顶部「密钥」导入 OpenSSH/PEM 私钥，加密私钥需填写口令；多台主机可以选同一密钥。生成新密钥后将公钥安装到远程用户的 `~/.ssh/authorized_keys`。文件夹和子文件夹用于组织主机，具体步骤见 [SSH 密钥与主机文件夹](ssh-keys-and-folders.md)。

自建中继与已绑定主机的中继迁移使用受控端 `setup --derp-config` / `connect --refresh-endpoint`，见 [配置与恢复步骤](tailcat-quickstart.md#自建中继与端点迁移)。网站默认 HTTP，不需要配置网站域名。
