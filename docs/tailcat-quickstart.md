# 使用 Tailcat 接入远程主机

在浏览器中选择「添加主机 → Tailcat 内网穿透」，按「安装 CLI → 后台运行 → 绑定主机」完成接入。`herdrx` 是用 Go 编写的远程主机 CLI，负责后台访问服务和生成一次性绑定凭据。网站运行在工作台主机，以下命令全部在**远程主机**上、以运行 Herdr 的同一用户执行。

支持 Linux x86_64（amd64）和 ARM64（arm64）；推荐使用提供 systemd 用户会话的常规 Linux 发行版。Herdr 需要自行安装并独立运行，参考 [Herdr 官方安装说明](https://herdr.dev/docs/install/)。herdrx 安装器不安装、升级或启动 Herdr。

## 1. 下载并安装 CLI

下载地址：[herdrx GitHub Releases](https://github.com/riba2534/herdrx/releases)。网页会检查是否存在附件齐全的 Release；尚未发布或暂时无法检查时，会显示对应提示。不要将源码 ZIP 当作 CLI 安装包。

每个 CLI Release 包含：

| 附件 | 用途 |
|---|---|
| `herdrx-linux-amd64.tar.gz` | Linux x86_64 |
| `herdrx-linux-arm64.tar.gz` | Linux ARM64 |
| `install-herdrx.sh` | 自动选择架构、校验并安装 CLI |
| `SHA256SUMS` | 附件 SHA-256 校验值 |
| `README-CLI.md` | 本教程的离线副本 |
| `release.json` | 版本、源码提交及构建信息 |
| `herdrx-linux-*.manifest.json` / `RELEASE-PUBLIC-KEY` | 签名更新清单与供核对的发行公钥 |
| `LICENSE` / `THIRD_PARTY_NOTICES.md` / `sbom.cdx.json` | 项目许可、依赖声明与依赖清单 |

推荐直接复制网站第一步提供的 **GitHub 一行安装命令**。页面自动填写可用版本，无需替换工作台地址；当前预发布版示例：

```sh
curl -fsSL https://github.com/riba2534/herdrx/releases/download/v0.1.0-rc.2/install-herdrx.sh | sh -s -- --version v0.1.0-rc.2
```

页面选择附件齐全的最新正式版本；只有预发布时自动选择 RC，并将脚本下载地址与安装版本固定到同一标签。脚本和安装包均直接从 GitHub 获取，安装过程不依赖任何工作台或个人域名。脚本自动识别架构、下载、校验并安装，不需要手动填写版本或配置 PATH。

安装脚本需要 curl、tar、coreutils（或 shasum），默认安装到 `~/.local/bin/herdrx`。后续教程直接使用这个路径；若想直接输入 `herdrx`，可自行将 `export PATH="$HOME/.local/bin:$PATH"` 加入 shell 配置。

需要安装其他版本时，将下载地址和 `--version` 的标签同时换成对应 [GitHub Release](https://github.com/riba2534/herdrx/releases) 的版本。也可以先下载 `install-herdrx.sh` 再运行 `sh install-herdrx.sh --version v0.1.0-rc.2`。自定义安装位置可追加 `--install-dir DIR`，后续命令使用相应路径。`latest` 只指向正式版，不包括 RC。

手动安装：从同一个 Release 下载适合 CPU 的包和 `SHA256SUMS`，在下载目录执行以下命令。ARM64 将示例中的 `amd64` 改成 `arm64`。

```sh
(
  set -eu
  archive=herdrx-linux-amd64.tar.gz
  awk -v name="$archive" '$2 == name { print; count++ } END { if (count != 1) exit 1 }' SHA256SUMS > archive.sha256
  sha256sum --check archive.sha256
  tar -xzf "$archive" herdrx VERSION README.md
  ./herdrx version
  install -d "$HOME/.local/bin"
  install -m 755 herdrx "$HOME/.local/bin/herdrx"
)
export PATH="$HOME/.local/bin:$PATH"
```

`SHA256SUMS` 用于检查下载内容是否与同一 Release 一致。Release 另附每种架构的 `.manifest.json` 签名清单，供内置 Ed25519 公钥的 CLI 校验更新。首次安装通过 GitHub HTTPS 获取对应版本的安装器和安装包。

## 2. 配置后台运行

确认当前用户可以正常使用 Herdr，再运行：

```sh
~/.local/bin/herdrx setup && ~/.local/bin/herdrx status
```

`setup` 检查 Herdr 路径、API 能力和已有守护进程，保存受控端身份，安装 `herdrx.service` 用户服务并等待它就绪。重复执行会保留已有身份与绑定。`status` 应显示 herdrx daemon「运行中」、Herdr 状态「ok」。Herdr 不在 PATH 中时用 `~/.local/bin/herdrx setup --herdr-bin /path/to/herdr` 指定实际路径。

检查关闭 SSH 及开机后的保活设置：

```sh
loginctl show-user "$(id -un)" --property=Linger
```

结果需要为 `Linger=yes`。若为 no，运行下面命令后重新检查；权限不足时，在同一用户的终端给该命令加上 `sudo`，或请管理员为这个用户启用 linger。不要用 `sudo herdrx setup`，否则会配置成另一个用户的服务。

```sh
loginctl enable-linger "$(id -un)"
```

服务管理与排查：

```sh
~/.local/bin/herdrx status
~/.local/bin/herdrx doctor
~/.local/bin/herdrx logs -n 100
~/.local/bin/herdrx logs -f
~/.local/bin/herdrx service restart
```

没有 systemd 用户会话时，执行 `~/.local/bin/herdrx setup --skip-service`，再用你自己的进程管理器运行 `~/.local/bin/herdrx serve`。直接在前台执行 serve 后关闭终端，会中断 Tailcat 访问。无法配置保活时也可以使用网站的 SSH 接入。

## 3. 绑定主机

优先复制网页第三步生成的命令。CLI v0.1.0-rc.2 起，公网 HTTPS 工作台可提供自带中继，命令会附带 `--workbench` 与 `--relay-token`。远程主机探测失败时会在生成绑定凭据前回退公共中继。临时中继授权 20 分钟有效，请勿分享；命令过期后从网页重新复制。已有未过期的绑定凭据会复用原区域，需要重新选择时给新命令追加 `--renew`。

直接使用公共中继或已有自定义配置时，在远程主机上执行：

```sh
~/.local/bin/herdrx connect --plain
```

将完整的 `herdrx://v1/...` 内容粘贴到网站第三步「绑定凭据」，可选填写主机显示名称和 Herdr 命名会话，点击「绑定并打开主机」。这是首次授权使用的一次性凭据：10 分钟内有效，只能绑定一次，不要分享或粘贴进公开日志。页面受理后会清除凭据，只保留配对任务标识以便刷新续接。

凭据过期时重新运行 connect。主动撤销尚未使用的旧凭据并生成新凭据：

```sh
~/.local/bin/herdrx connect --renew --plain
```

绑定成功后，正常重启或更新 herdrx 无需重新绑定。远程 Herdr 与任务独立运行；关闭浏览器、退出网站或工作台主机离线，只影响访问连接。远程主机自身重启后的任务恢复能力由 Herdr 和任务本身决定，启用 linger 不代表 Shell 进程能跨主机重启存活。

## 更新与移除

已安装的 CLI 可以直接从 GitHub 检查和安装签名版本：

```sh
~/.local/bin/herdrx update --version v0.1.0-rc.2 --check
~/.local/bin/herdrx update --version v0.1.0-rc.2
~/.local/bin/herdrx status
```

正式版发布后，省略 `--version` 会选择最新正式版。更新会校验签名、兼容性和当前身份，重启访问服务并检查就绪；失败时恢复旧程序，已有身份、绑定与远程任务保留。`~/.local/bin/herdrx rollback` 切回最后一个兼容程序，详见 [更新与恢复](update-and-recovery.md)。

重新运行安装器也可替换 CLI：校验后原子安装，旧文件保存为同目录的 `herdrx.previous`，随后需手动执行 `~/.local/bin/herdrx service restart`。此文件备份与签名更新的回退点不同。配置目录 `~/.config/herdrx` 不会被替换；不要在下载临时目录中运行 setup。

`~/.local/bin/herdrx service stop` 暂停访问服务；`~/.local/bin/herdrx service uninstall` 移除该用户服务。二者不停止 Herdr 或结束 pane 内任务，也不删除身份配置。解除已绑定的访问授权使用 `~/.local/bin/herdrx unpair`，会让当前 Tailcat 访问失效，下一次接入需要重新绑定。

## 常见问题

| 现象 | 处理 |
|---|---|
| 安装下载失败 | 检查远程主机能否访问 GitHub，以及下载地址和 `--version` 是否指向同一个已发布版本 |
| `herdrx: command not found` | 设置 PATH，或用 `~/.local/bin/herdrx version` 确认安装位置 |
| `setup` 提示缺少或未运行 Herdr | 按 Herdr 官方说明安装并启动；使用相同用户运行 setup |
| `systemctl --user` 无法连接总线 | 用正常 SSH 用户登录会话，检查 systemd 与用户环境；必要时使用自己的进程管理器 |
| 退出 SSH 后无法连接 | 检查 linger，确认后台服务运行；重新登录后查看日志 |
| `connect` 提示 daemon 未运行 | 运行 setup 或 `~/.local/bin/herdrx service start`，通过 status 确认就绪 |
| 已有旧版配置迁移失败 | 按错误修复旧配置或停止旧版 herdrx-agent 服务后重试；不会静默生成新身份 |
| 绑定超时 | 检查远程主机和工作台主机的网络，运行 doctor / logs；刷新网页续接已有任务 |

## 自建中继与端点迁移

网站默认 HTTP，直接使用公共或手动配置的中继。部署者另行配置了公网 HTTPS 时，可使用[工作台自带中继](operations.md#工作台自带中继)，从原主机卡片「更新连接端点」复制命令并导入输出包。选择自建 DERP 时，在远程主机准备 `derp.json`，使用实际中继主机替换示例地址：

```json
{
  "RegionID": 1,
  "RegionCode": "private",
  "RegionName": "My relay",
  "Nodes": [
    { "Name": "relay-1", "HostName": "derp.example.com", "DERPPort": 443, "STUNPort": 3478 }
  ]
}
```

配置只接受一个区域，最多 8 个节点、16 KiB JSON。节点须为可公开访问的中继；地址经过校验后固定用于拨号，未知字段、私网/元数据地址与测试证书跳过选项会被拒绝。可填写与主机名证书匹配的中继；单独 DERP 的 TLS 属于中继协议，网站仍提供 HTTP。

首次安装：

```bash
~/.local/bin/herdrx setup --derp-config ./derp.json
~/.local/bin/herdrx connect --plain
~/.local/bin/herdrx doctor --network
```

已有绑定需要更换中继时，准备新配置后执行：

```bash
~/.local/bin/herdrx connect --refresh-endpoint --derp-config ./derp-new.json --plain
```

在原网站的该主机卡片选择「更新连接端点」，导入输出的更新包。它使用已有 SSH 主机身份签名，绑定原 agent、controller 和授权，10 分钟内有效。网站只允许主机所有者导入，并校验身份、PSK、递增版本和中继地址；输入包不会新增主机或授权新网站。丢失网页响应时可重新导入同一包，过期后重新执行 `~/.local/bin/herdrx connect --refresh-endpoint --plain`。

迁移会重建 herdrx 的访问端点，远程 Herdr 和任务继续运行。新区域先持久化，再启动监听，进程重启继续使用新配置；若命令提示监听启动失败，可检查配置并重新执行，或者明确指定原配置恢复端点，再向网站导入相应的新版本更新包。不要通过恢复旧身份备份来撤销迁移。主机身份丢失时仍需重新配对；跨区域自动发现不属于本功能。
