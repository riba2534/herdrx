# 安装 herdrx

网站入口是 `herdrx-server`，受控端 CLI 是 `herdrx`。网站 Docker 镜像由 GitHub Actions 发布到 ZOT。

## 网站

按 [README 快速开始](../README.md#快速开始) 准备部署文件、初始化本地目录、登录自己的镜像源后，在源码根目录执行：

```bash
docker compose --env-file deploy/.env -f deploy/compose.yml pull
docker compose --env-file deploy/.env -f deploy/compose.yml up -d --wait
```

`deploy/compose.yml` 是唯一生产定义，`compose.prod.yml` 是兼容符号链接。生产目标机不用源码构建，开发才叠加 `compose.dev.yml`。

数据位于部署目录 `./data`，以 bind mount 挂到 `/data`；没有 Docker 命名卷。默认提供 HTTP，无需域名；如需 HTTPS，由部署者自配反向代理，详见 [运维](operations.md)。镜像必须先完成 Actions 发布，不能将尚未存在的标签当成已上线产物。

## 受控主机

先安装并独立运行 Herdr，以运行 Herdr 的同一用户操作。网站「添加主机 → Tailcat 内网穿透」提供完整的三步引导：

1. 从 [GitHub Releases](https://github.com/riba2534/herdrx/releases) 安装 `herdrx`；Linux x86_64 / ARM64 附件带 SHA-256 校验和离线教程。页面只为已检测到的完整版本生成安装命令。
2. 执行 `herdrx setup && herdrx status`，确认用户后台服务就绪，再检查 `loginctl show-user "$(id -un)" --property=Linger`；需为 `Linger=yes`。未启用时执行 `loginctl enable-linger "$(id -un)"`，权限不足再加 sudo。
3. 执行 `herdrx connect --plain`，在页面最后一步填写一次性绑定凭据并打开主机。

所有复制命令、手动安装、PATH 设置、无 systemd 环境、升级及排障见 [Tailcat 接入教程](tailcat-quickstart.md)。网页根据实际 Release 生成固定版本命令；预发布需要明确选择 RC 标签。开发者可通过 `make build` 获取本机架构的 `bin/herdrx`。

已有自定义 unit 时 setup 拒绝覆盖；旧配置损坏或旧服务仍在运行时，迁移明确失败，不会创建替代身份。Linux amd64/arm64 的原生 systemd 生命周期、升级回滚和旧服务迁移已通过隔离 guest 验收；macOS 受控端完整服务支持不在首发范围。详见 [当前验收](release-validation-2026-09-07.md)。

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

## SSH 密钥与分组

Herdr 必须已由该用户独立安装并运行。终端观察命令先使用非交互 SSH 的 `PATH`，再查找 `~/.local/bin` 和 `/usr/local/bin`，不依赖交互式 Shell 的初始化文件。安装在其他目录时，请配置远程非交互 SSH 的 `PATH`。终端观察进程退出会显示原因；修复远程环境后点击「重连终端」，不会重新创建 pane 或重放关闭期间的输入。

在「添加主机 → SSH」填写地址、自定义端口和 SSH 用户，选择密码认证或已保存密钥。已有密钥先通过主机页顶部「密钥」导入 OpenSSH/PEM 私钥，加密私钥需填写口令；多台主机可以选同一密钥。生成新密钥后将公钥安装到远程用户的 `~/.ssh/authorized_keys`。文件夹和子文件夹用于组织主机，具体步骤见 [SSH 密钥与主机文件夹](ssh-keys-and-folders.md)。

自建中继与已绑定主机的中继迁移使用受控端 `setup --derp-config` / `connect --refresh-endpoint`，见 [配置与恢复步骤](tailcat-quickstart.md#自建中继与端点迁移)。网站默认 HTTP，不需要配置网站域名。
