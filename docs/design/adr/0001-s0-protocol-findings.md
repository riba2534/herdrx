# ADR 0001: S0 herdr 协议实测结论

日期：2026-09-04

## 状态

Accepted for implementation; tailcat 容量与跨机器故障注入仍需在集成阶段继续验证。

## 实测

- herdr 0.8.2 的 `~/.config/herdr/herdr.sock` 可直接收发 NDJSON；`session.snapshot` 返回 protocol 20、workspace/tab/pane/layout/agent 全量状态。
- `events.subscribe` 的确认响应后会出现早于当前快照的 workspace/layout 事件，事件结构没有统一可比较 revision。
- `terminal session observe` 首帧为 `full:true`，携带 seq、width、height 和 base64 ANSI。
- 在临时、非聚焦 shell pane 上，`terminal session control` 无需 takeover 即可得到 full frame、接收输入、产生增量帧并通过 `terminal.release` 以 `reason=detached` 关闭。
- tailcat HEAD `476c217fa9fa5b304cdb7f07404a6c3844eac0b0` 的全量 Go tests 通过，要求 Go 1.27。
- 2026-09-04 的 `tailcat.dev` map 只有 301/302/303/304 四个区域；硬编码 region 1 会启动失败，公共试用默认暂取东京 304，并允许环境变量覆盖。生产仍应使用自建 DERP 长地址。
- 浏览器通过真实 tailcat Client、受限 SSH agent 接入同一 Herdr，会话 snapshot、observe、control、input 与 release 全部通过；测试输入 `HERDRX_TAILCAT_OK` 在目标 pane 原样读回。
- 原版 Herdr TUI 已在本机 PTY和 tailcat agent 的 SSH PTY 两条路径渲染成功；tailcat 路径的窗口尺寸只通过 `pty-req/window-change` 协商，不进入命令字符串。
- distroless nonroot Docker 镜像能够初始化 `/data`，内置 healthcheck 返回 healthy；最终测试镜像约 11.5 MB。

## 决策

1. 结构状态以 fresh snapshot 为权威；herdr 事件只作为低延迟刷新信号，不直接把无 revision 的历史事件归并进新快照。
2. herdrx 终端帧保留 `full/seq/width/height`，WebSocket wire 不使用原 v0.3 的两字节裸 payload。
3. 每个 control stream 必须有服务端租约；进程退出或租约过期都发送 release。
4. 远端 socket 必须使用远端绝对路径，不能把 `~` 直接传给 streamlocal。
5. S0 的临时代码不构成稳定 API；实现以自动测试和本 ADR 为约束。
6. 同一个 tailcat node key 的 Client 必须按 host 复用；REST、WebSocket 和后台通知不得各自创建并发网络引擎。
