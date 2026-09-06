# 更新、回滚与恢复

## 网站 Docker 更新

GitHub main 推送后自动验证并发布 ZOT 镜像，目标机决定何时部署。先按 [运维文档](operations.md#数据与备份) 备份，在部署目录执行：

```bash
docker compose pull
docker compose up -d --wait
docker compose exec herdrx /app/herdrx-server healthcheck
```

`restart` 不会换镜像。更新复用本地 `./data` bind 目录，不需要创建、迁移或删除 Docker 卷。不能同时启动两个网站实例挂同一目录。

每次 Actions 发布附件包含带域名占位符的 `release.env` 和验证记录。使用时将 `registry.example.com` 替换为自己的镜像源，保留升级前的 digest；回退时将 `.env` 的 `HERDRX_IMAGE` 改为旧的 `registry.example.com/herdrx/server@sha256:...`，再 pull/up。

镜像回退不等于数据回退。如果新版本修改了数据库且旧版本不能兼容，应停止网站并从升级前一致性备份恢复完整数据，不要只替换数据库或主密钥中的一项。先在隔离副本验收恢复；当前尚未完成正式候选的异机恢复测试。

## CLI 更新

GitHub Release 安装器采用普通文件的原子替换：先下载并校验对应架构压缩包、包内版本和 CLI 版本，再安装到 `~/.local/bin/herdrx`；不同的旧文件保留为 `herdrx.previous`。重复安装相同内容不替换文件。安装器不自动重启后台服务；确认后执行 `herdrx service restart` 和 `herdrx status`，远程身份和绑定配置保留。完整命令见 [Tailcat 接入教程](tailcat-quickstart.md)。

首次安装通过 GitHub HTTPS 和 SHA-256 校验取得程序；后续更新使用程序内置的 Ed25519 发行公钥验证清单。可信公钥来自源码中的 [`internal/updater/release.pub`](../internal/updater/release.pub)，不能用同一次下载返回的新公钥替换它。安装器的 `herdrx.previous` 是普通文件备份；第一次签名更新会把当前程序纳入版本目录，之后由 `herdrx rollback` 管理回退点。

```sh
herdrx update --check                 # 只验证最新正式版本的清单
herdrx update                         # 下载、验签并安装最新正式版本
herdrx update --version v0.1.0-rc.1    # 明确选择预发布或固定版本
herdrx rollback                       # 回到已验证的兼容版本
```

没有正式 Release 时，默认更新会明确报告下载失败；预发布必须使用 `--version` 指定。下载有超时和大小限制，先校验清单签名、平台、协议、状态格式和时效，再从该版本标签下载程序。清单覆盖的是二进制 SHA-256，归档不向任意路径解包。清单默认有效 180 天，过期后不能继续安装；维护者需发布新版本，不覆盖已发行的包。`--check` 不修改服务或安装文件。

离线更新先下载对应架构压缩包和 `.manifest.json`，在独立目录解包后执行 `herdrx update --manifest ./herdrx-linux-amd64.manifest.json --binary ./herdrx`。私有发行才使用独立可信渠道取得的 `--pubkey`。默认拒绝降级；明确需要时用 `--allow-downgrade`，仍需通过当前身份格式兼容检查。

更新持有进程锁，将程序放入不可变版本目录；候选自检能读取当前身份后，才切换稳定入口并重启 `herdrx.service`。30 秒内用本地 IPC 核验程序版本、身份及撤销状态；失败会恢复旧入口、重启旧服务并核验。中断留下的事务在下一次 update/rollback 时恢复；恢复成功后按提示重新执行更新。同版本、相同内容的重试不覆盖上一个回退点，同版本不同内容直接拒绝。

使用自定义 XDG 路径或安装目录时，保留 setup 对应的 `--config`、`--runtime-dir`、`--stable-link` 和 `--releases-dir`。自定义 systemd unit 若无法确认受本工具管理，会给出修正指引。CLI 更新、停止及回滚只操作 herdrx 访问服务，不升级或停止独立运行的 Herdr，不恢复身份文件，不降低已经撤销的 binding/epoch。外网故障不作为盲目回滚理由。

滚动闪烁修复增加受控端只读终端尺寸命令及 Herdr client socket 转发。Tailcat 连接需同步安装配套 CLI 并使 herdrx 访问服务运行新二进制；仅替换网站不会升级远程 CLI。旧版缺少该能力时，全屏滚动会提示更新，网页观察和终端任务继续保留。基础 SSH 无 CLI 依赖，远程需有 Python 3。详见[兼容要求](install.md#终端滚动兼容要求)。

## 配对恢复

- 网页受理后的任务通过任务 ID 查询状态；对异常刷新和未完成任务的恢复仍需完成候选验收。
- Agent 的 prepared/active 状态与未过期 enrollment 应从持久化身份恢复，不随意生成替代凭据。
- commit 应答丢失时，使用原正式钥查询/提交，避免创建重复绑定。
- `unpair` 先持久化撤销并关闭访问连接，再用最多 2 秒排空网络关闭报文；不会为了等待离线对端而无限阻塞，也不会停止 Herdr 任务。
- 删除网页主机不等于远端撤销。在受控机执行 `herdrx unpair`，再次接入执行 `herdrx connect`。

## 未完成的验收

真实跨 NAT、禁 UDP 后的跨网恢复、72 小时运行、异地物理主机数据恢复和公开签名分发仍待验收。Linux 双架构 CLI 事务升级、回滚、guest 重启恢复及网站容器替换已经通过；备份恢复已在干净目录中验证原身份、绑定及加密通知。详细范围见 [当前验收](release-validation-2026-09-07.md)，短时本地结果不能抵扣长期或真实跨网项目。

## 访问管理迁移与旧版回退

本次将 SQLite WAL 的同步级别设为 `FULL`，让登录撤销、邀请和主机配对事务在返回成功前同步落盘。整机掉电验收曾在原 `NORMAL` 配置下丢失近期登录及主机记录；该配置的事务持久性差异见 [SQLite 官方文档](https://www.sqlite.org/pragma.html#pragma_synchronous)。

当前网站使用 SQLite schema 3，可从 schema 0/1/2 迁移。schema 3 增加可复用 SSH 密钥元数据、主机文件夹及归属引用，既有主机初始归入「未分组」，原凭据与登录保持。schema 1 增加邀请使用/撤销字段和列表索引，schema 2 增加持久化注册设置，首次迁移固定为关闭注册。已有管理员需在「管理」页面手动开启，旧 `HERDRX_REGISTRATION` 环境变量不能打开注册。后续重启保留管理员设置。迁移在一个事务内完成；失败则拒绝启动，不重建数据库。既有用户、密码哈希、登录、主机、加密凭据与配对标识保持原值。普通用户历史上的本机记录保留，但不再允许访问。更高版本的数据库拒绝由此版本打开。

升级前停止网站并备份完整数据目录及配置，使用候选镜像启动数据副本验证迁移，再执行正常镜像更新。网站停止期间远程主机上的 Herdr 和任务继续运行。备份必须包含 `master.key`（或保留相同的外部主密钥），否则无法解密主机凭据。

**不能仅切回旧镜像就认为访问撤销仍然有效。** schema 2 二进制会拒绝 schema 3，schema 1 二进制会拒绝 schema 2，不能手动降低版本号绕过检查。更早版本还可能缺少邀请撤销或长连接撤销逻辑。优先修复并向前升级。确需恢复旧快照时，在网站停止期间清空快照中的 `sessions`、`invites` 和 `push_subscriptions`，复核并重新应用备份后发生的用户禁用；使用目标版本实际支持的方式关闭注册，再启动经过评估的版本。schema 2/3 从数据库读取开关，schema 1 才使用旧环境变量。之后所有用户重新登录，通知需重新订阅，避免备份复活已撤销的访问。旧版缺失的权限保障不会因恢复数据而补齐。

工作台主机丢失后，恢复其数据、主密钥与配置才能继续使用已保存的连接信息；远程 Herdr 会话不需要重建。恢复页面时应连接原 pane，不重新执行之前的命令。

网站默认部署仅提供 HTTP。现有自建反向代理由部署者独立维护；升级网站不管理代理或证书。旧部署若已自行启用 Caddy，可保留原代理配置并单独管理，新的部署附件不再附带 TLS 服务。

更换 DERP 时，使用 [签名端点更新](tailcat-quickstart.md#自建中继与端点迁移) 保留原绑定。端点版本保存在受控端配置与网站加密凭据内，备份时一并保存；不要回退版本计数或恢复旧授权来绕过重放检查。
