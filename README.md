<p align="center">
  <img src="web/public/brand/icon-128.png" alt="herdrx Logo" width="128" />
</p>

<h1 align="center">herdrx</h1>

<p align="center">
  <strong>自托管、多用户的 Herdr 远程终端工作台</strong>
  <br />
  在电脑和手机上连接多台主机，继续同一组工作区、分屏终端与 Agent 任务。
</p>

<p align="center">
  <a href="https://hub.docker.com/r/riba2534/herdrx"><img src="https://img.shields.io/docker/pulls/riba2534/herdrx?style=for-the-badge&logo=docker&label=Docker%20Hub" alt="Docker Hub" /></a>
  <a href="https://github.com/riba2534/herdrx/releases"><img src="https://img.shields.io/github/v/release/riba2534/herdrx?include_prereleases&style=for-the-badge" alt="Release" /></a>
  <img src="https://img.shields.io/badge/Linux-amd64%20%7C%20arm64-2496ED?style=for-the-badge&logo=linux&logoColor=white" alt="Linux amd64 / arm64" />
  <a href="https://github.com/riba2534/herdrx/stargazers"><img src="https://img.shields.io/github/stars/riba2534/herdrx?style=for-the-badge&color=f5a623" alt="GitHub Stars" /></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-0F766E?style=for-the-badge" alt="MIT License" /></a>
</p>

<p align="center">
  <a href="#快速开始">快速开始</a> ·
  <a href="#接入远程主机">接入主机</a> ·
  <a href="#日常使用">日常使用</a> ·
  <a href="#更新与数据">更新与数据</a> ·
  <a href="#常见问题">常见问题</a>
</p>

---

herdrx 是 [Herdr](https://herdr.dev/) 的 Web 客户端，适合个人和可信小团队。把运行 Herdr 的主机接入后，你可以在电脑和手机上查看 Agent 状态、切换工作区、操作分屏终端。

**Herdr 和任务独立运行在远程主机上。** 关闭浏览器、退出账号或重启网站后，远程任务继续运行；重新进入即可连接原有会话。远程主机自身的重启与任务恢复由 Herdr 和任务管理器负责。

当前为预发布阶段，版本与已知限制见 [发布说明](https://github.com/riba2534/herdrx/releases)。

## 你可以做什么

| 功能 | 使用方式 |
|---|---|
| 管理多台主机 | 使用 SSH 或 Tailcat 接入，按名称和地址搜索，通过文件夹分组 |
| 操作终端 | 切换工作区、Tab 与 Pane，使用分屏、历史滚动、中文输入和 Shift+Enter |
| 向 Agent 传图片 | 粘贴、拖入或上传 PNG/JPEG/WebP/GIF，单张最大 20 MB |
| 在手机上继续任务 | 单终端视图、辅助键条、字号与缩放设置 |
| 调整外观 | 独立切换网站与工作台主题，选择终端配色 |
| 复用 SSH 密钥 | 导入或生成密钥，在多台主机中选择使用 |
| 邀请团队成员 | 管理员开启邀请注册；每个账号管理自己的主机和密钥 |
| 安装为应用、接收通知 | 通过浏览器安装到桌面或手机，按浏览器支持启用通知 |

## 快速开始

部署网站的机器需要 **Linux（amd64 或 arm64）和 Docker Engine**。运行任务的远程主机需要提前安装并运行 Herdr。

### 1. 一键 Docker 部署

下列命令创建部署目录、准备数据权限，并从 [Docker Hub](https://hub.docker.com/r/riba2534/herdrx) 拉取公开镜像启动网站：

```bash
mkdir -p herdrx && cd herdrx &&
sudo install -d -m 700 ./data &&
sudo chown 65532:65532 ./data &&
docker run -d \
  --name herdrx \
  --restart unless-stopped \
  --pull always \
  --security-opt no-new-privileges:true \
  -p 8080:8080 \
  -e HERDRX_PUBLIC_URL=http://localhost:8080 \
  --mount type=bind,source="$(pwd)/data",target=/data \
  riba2534/herdrx:latest
```

**在另一台电脑或手机上访问时，先把命令中的 `http://localhost:8080` 改成工作台主机的实际 IP 地址和端口。** 这个地址必须与你在浏览器中打开的地址一致。默认使用 HTTP，无需域名或证书。

镜像自动匹配 amd64 / arm64。数据保存在当前部署目录的 `./data` 中。上面的 sudo 用于将目录交给镜像默认用户 `65532:65532`；这个数字并非固定要求，也可使用宿主机的普通用户运行，见 [目录权限](docs/operations.md#目录权限与原生实例迁移)。

### 2. 创建管理员

在刚才的部署目录读取初始化令牌：

```bash
sudo cat ./data/bootstrap-token
```

打开 [http://localhost:8080](http://localhost:8080)（远程访问使用你配置的地址），填写令牌并创建管理员。创建成功后令牌文件自动删除，之后使用邮箱和密码登录。

注册默认关闭。需要其他账号时，在「管理 → 用户注册」开启邀请注册，再创建邀请码交给成员。

### 3. 接入主机

登录后点击「添加主机」，选择下面任一方式。网站不会安装、升级或停止远程 Herdr。

## 接入远程主机

### SSH

适合工作台主机能够直接通过 SSH 访问的远程主机。

1. 在「添加主机」中选择 SSH，填写地址、端口和用户名。
2. 选择密码或密钥认证；已有私钥可先在「密钥」页导入。
3. 核对首次连接的 SSH 指纹，保存后打开主机。

连接用户应为运行 Herdr 的用户；远程 sshd 需允许 Unix socket 转发，全屏应用滚动还需要 Python 3。安装在特殊路径、密钥与证书配置见 [安装说明](docs/install.md#ssh-密钥与分组)。

### Tailcat 内网穿透

适合无法从工作台直接 SSH 访问的 Linux 主机。CLI 支持 x86_64 / ARM64。

在「添加主机 → Tailcat 内网穿透」中，按页面提供的三步引导操作：

1. 以运行 Herdr 的同一用户，在远程主机执行页面中的 CLI 安装命令。
2. 执行 `herdrx setup && herdrx status`，按提示启用后台保活。
3. 执行 `herdrx connect --plain`，将一次性绑定凭据粘贴到页面并连接。

绑定凭据 10 分钟内有效且只能使用一次，请勿公开分享。完整安装、后台运行、手动下载及排障见 [Tailcat 接入教程](docs/tailcat-quickstart.md)。

## 日常使用

进入主机后，选择原有工作区和 Tab，在对应 Pane 中继续操作。电脑上使用分屏查看多个终端，手机上切换当前终端并使用底部辅助键条。

图片可以直接粘贴、拖入或上传。上传后不会自动按回车；支持附件的 Agent 可以读取图片，普通 Shell 收到的是文件路径。

在主机列表通过文件夹整理主机；在「密钥」页管理可复用的 SSH 密钥。详细步骤见 [SSH 密钥与文件夹](docs/ssh-keys-and-folders.md)。

从浏览器菜单安装 herdrx，可作为独立窗口使用。安装和后台通知需要浏览器支持；普通 HTTP/IP 可以使用网站和终端，PWA 与通知通常需要 HTTPS 或本机 localhost。需要 HTTPS 时可接入自己的反向代理，见 [运维说明](docs/operations.md#https)。

## 更新与数据

`riba2534/herdrx:latest` 提供最新通过验证的主分支镜像。已有容器不会自动更新；先按 [备份说明](docs/operations.md#数据与备份) 备份，再在**原部署目录**执行下面的命令，并保留原来的访问地址、端口和其他配置：

```bash
docker pull riba2534/herdrx:latest &&
docker stop herdrx && docker rm herdrx &&
docker run -d \
  --name herdrx \
  --restart unless-stopped \
  --security-opt no-new-privileges:true \
  -p 8080:8080 \
  -e HERDRX_PUBLIC_URL=http://localhost:8080 \
  --mount type=bind,source="$(pwd)/data",target=/data \
  riba2534/herdrx:latest
```

重建容器复用原 `./data`，保留账号、主机和密钥。**不要删除数据目录，也不要让两个实例同时使用它。** 备份必须包含完整数据库、`master.key` 和通知身份；固定版本、回退与恢复见 [更新与恢复](docs/update-and-recovery.md)。

常用管理命令：

```bash
docker logs --tail 100 herdrx                       # 查看日志
docker exec herdrx /app/herdrx-server healthcheck   # 检查健康状态
docker stop herdrx                                 # 停止网站
docker start herdrx                                # 启动网站
```

需要额外配置时，在 `docker run` 中加入 `-e 变量名=值` 后重建容器：

| 配置 | 用途 |
|---|---|
| `HERDRX_PUBLIC_URL` | 浏览器实际访问的网站地址 |
| `HERDRX_COOKIE_SECURE=true` | 自行配置 HTTPS 后启用 |
| `HERDRX_ALLOWED_ORIGINS` | 额外允许的完整访问地址，多个用逗号分隔 |
| `HERDRX_TRUSTED_PROXIES` | 反向代理的出口 IP/CIDR，直接访问时留空 |
| `HERDRX_MAX_HOST_CONNECTIONS=20` | 全实例同时连接的主机上限 |
| `HERDRX_HOST_DIAL_CONCURRENCY=4` | 同时建立连接的数量上限 |
| `HERDRX_SESSION_TTL=720h` | Web 登录有效期 |

## 常见问题

**登录提示 `origin_forbidden`？** 检查 `HERDRX_PUBLIC_URL` 是否与浏览器地址一致，包括协议和端口。修改环境变量后需要重建容器。

**8080 端口已占用？** 把 `-p 8080:8080` 改成例如 `-p 18080:8080`，同时把 `HERDRX_PUBLIC_URL` 的端口改为 `18080`。

**Docker 中如何接入宿主机 Herdr？** 使用 SSH 或 Tailcat。网站的「本机」指容器内部，不能直接访问宿主机上的 Herdr。

**关闭网站会停止任务吗？** 远程 Herdr 与任务继续运行；网站恢复后重新连接原会话。网站与 Herdr 同机运行时，共享整机故障边界。

**SSH 支持跳板机和 SSH alias 吗？** 当前使用直接填写的地址、端口、用户名与密码/密钥，不支持 SSH alias、ProxyJump、FIDO、GSSAPI 或 ssh-agent 转发。

**可以共享主机或部署多个网站副本吗？** 当前每台接入记录归属一个账号，使用单实例部署。请在可信环境中使用：实例管理员能够接触终端内容和解密后的连接凭据。

## 更多使用说明

- [安装与兼容要求](docs/install.md) · [Tailcat 接入教程](docs/tailcat-quickstart.md)
- [SSH 密钥与文件夹](docs/ssh-keys-and-folders.md)
- [运维与备份](docs/operations.md) · [更新与恢复](docs/update-and-recovery.md)
- [版本与发布说明](https://github.com/riba2534/herdrx/releases) · [反馈问题](https://github.com/riba2534/herdrx/issues)

## 致谢与许可

感谢 [Herdr](https://herdr.dev/) 和 [Tailcat](https://github.com/tailscale/tailcat)。herdrx 使用 [MIT 许可证](LICENSE)，第三方依赖声明见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
