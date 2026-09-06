# 2026-09-07 发布准备验收

当前验证对象为 `v0.1.0-rc.1` 本地候选。源码、二进制及容器尚未公开发布；完整状态以 [发版清单](release-readiness-2026-09-06.md) 为准。

## 本轮通过的检查

| 范围 | 证据与限制 |
|---|---|
| Go | vet、普通全量与 race 通过；CLI 高影响路径包含真实子进程和隔离 SSH/Tailcat |
| 前端 | typecheck、lint、14 个文件的 80 项 Vitest；生产资源重新生成 |
| 浏览器 | Chromium、Firefox、WebKit 的引导、密钥/文件夹、主题、自绘控件、显示及 PWA；实际网站登录撤销与重启，终端渲染、闪烁和手机触摸脚本通过 |
| CLI 原生系统 | amd64 Ubuntu KVM guest 与 Apple Silicon 上的原生 arm64 Linux guest；setup、重复 setup、SSH 登出、stop/start/restart/uninstall、旧服务迁移及失败恢复 |
| 更新和回滚 | 原生双架构签名 rc.1→rc.2、重复升级、rollback；实际服务 IPC 版本变化，原 Herdr 与 task PID 在访问服务操作中保持不变 |
| guest 重启 | 两种架构均恢复访问服务、身份和原 pane；远程主机自身 reboot 会终止 OS 进程，不承诺 task PID 跨 reboot 保持 |
| 网站镜像 | amd64/arm64 均在原生 CPU 运行：nonroot、默认 HTTP Cookie、bind、健康、静态资源、管理员初始化及重建后登录；独立测试代理的 HTTPS/WSS/来源和限流兼容通过 |
| 恢复 | SQLite 一致性备份和原 master.key/VAPID 恢复到干净数据目录；新 API/连接池使用原授权读取原 pane、发送加密通知；拒绝覆盖备份，0600 权限及同步落盘 |
| 容量 | 10/50/100 逻辑主机的真实 SSH 和 Tailcat 连接池测量；默认最多 20 个活动主机、4 个并行拨号，详见 [容量记录](capacity-validation-2026-09-07.md) |
| 依赖和产物 | 可达漏洞修复、pnpm audit、第三方声明和 CycloneDX；双架构 CLI 签名清单、归档校验和发布防护，详见 [依赖记录](dependency-review-2026-09-07.md) |

本轮发现并修复 PWA 缓存问题：Go 静态文件服务将 `/index.html` 重定向到 `/`，缓存中带重定向元数据的响应不能直接用于导航。Service Worker 现在构造保留内容和响应头的新响应，实际网站重启及三引擎离线/更新场景通过。

配对测试也补齐了 CLI、状态写入和 SSH 错误处理。撤销测试原先用旧内存快照生成签名、忽略首次 commit 失败，并通过不允许的命令推断连接撤销。现在直接采用 prepare 应答的挑战值，检查 active 应答和真实终端交互，再观察 SSH transport 是否关闭；超时不再计作成功。

Web Push 出站访问补齐了订阅 URL/密钥检查、HTTPS 443、公网 DNS 检查和固定 IP 拨号、禁止重定向及环境代理。旧实现可向用户提供的本机 HTTP 地址发送请求，已用隔离接收器复现；修复后请求在出站前被拒绝。现有禁用取消和恢复后加密通知回归保留真实 HTTP 传输，测试中的本地供应商由显式测试客户端映射，不开放生产绕过配置。

首次 SSH 接入曾在返回待确认指纹后导致网站崩溃：失败的 `*SSHEndpoint` 被转换为非 nil 的接口，连接池在关闭它时触发 panic。工厂现在在本机、SSH、Tailcat 的失败路径显式返回 nil 接口；真实 SSH 测试覆盖待确认指纹、持久化信任、三类认证成功及错误密码。

原生 amd64 SSH 与 arm64 Tailcat 已接入真实 Docker bridge 网站。隔离预检完成网站重建、关闭容器非 DNS 的 UDP 出站后重启访问服务并通过 TCP 中继读取原 pane、恢复 UDP 后重连；真实 Chromium 能打开两台主机的心跳终端，原 Herdr/task PID 保持。两台测试机仍处于同一本地网络，不计作不同运营商或蜂窝网络验收。

撤销的连续回归还发现用户态 TCP 关闭报文可能在 Tailcat 网络栈立即退出时丢失。`unpair` 现在先撤销并关闭访问连接，再在不占用 IPC 互斥锁的情况下为 TCP 排空保留最多 2 秒；离线对端或 TIME-WAIT 不会无限阻塞命令。测试仍检查控制端真实 SSH transport 关闭，不接受超时作为撤销成功。

持续运行预检改为检查真实终端输出帧后，又复现了普通 SSH 空白终端：非交互 SSH 的 `PATH` 缺少 `~/.local/bin`，而前端忽略了 `terminal.closed`。SSH 观察命令现在保留原 PATH 优先级并补充常用安装目录；Tailcat 保留受限命令语法。网站对正常 EOF、异常退出和协议关闭均发送一次终端关闭通知；前端保留错误和手动重连入口，停止向已关闭通道发送输入。回归覆盖关闭先于 UI 订阅、其他 pane 隔离、重试不重放输入，以及手机上的长错误信息和原终端尺寸保持。

## 首发兼容范围

| 组件 | 已验证范围 |
|---|---|
| 网站及受控 CLI | Linux amd64/arm64；单实例；可信管理员；单 owner/单网站绑定 |
| Herdr | 0.8.2；私有终端协议 20 真实运行；协议 22 只有独立协议样例核验 |
| Tailcat | Go 模块 `github.com/tailscale/tailcat v0.6.0`；传递依赖固定在 go.mod/go.sum |
| 网站数据库 | schema 3；旧 schema 0/1/2 的迁移和未来 schema 拒绝规则；不可兼容回退须按 [恢复步骤](update-and-recovery.md#访问管理迁移与旧版回退) 操作 |
| 基础 SSH 滚动 | Linux `/proc`、Python 3、同用户 Herdr API/client socket；不覆盖 ProxyJump、FIDO/GSSAPI |
| 浏览器客户端 | 三引擎自动化通过；真实 Mac/iOS/Android 安装和网络切换未执行 |

网站默认 HTTP，无需域名。PWA/Push 按浏览器安全上下文要求启用，本机用 localhost 验证；部署者需要 HTTPS 时自行配置代理。

## 尚未完成

首次 GitHub CI、ZOT 实际分发与公开 Release 下载、新用户独立照文档操作、不同 NAT/运营商故障矩阵、真实手机操作，以及 24 小时关闭浏览器和 72 小时运行记录仍缺证据。项目许可证由维护者决定。不能将原生 guest、回环中继或短时容器测试当作这些项目已经通过。

推送源码可用于启动首次 CI；正式公开版本需继续关闭上述适用条件。每个候选的提交、命令、退出码、产物摘要和测试日志应保存为独立验收记录，不能仅凭此滚动文档判断某个下载文件已通过。
