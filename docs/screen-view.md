# 画面视图与远程主机拉起 Herdr

日期：2026-10-06。本文记录两项改动的设计、边界与验证结果：pane 的**画面视图**，以及在远程主机上**启动 Herdr** 的两条路径（SSH 主机的网页按钮、受控端服务启动时自动拉起）。

## 画面视图

### 做什么

画面视图读取 Herdr 已经渲染好的 pane 文本，在浏览器里用普通文字显示；输入仍通过本地输入框和辅助键发到同一个 pane。它和 Terminal、Chat 一样只改变网页视图，不重启终端或 Agent。

与 xterm 终端视图相比：

| | 终端视图 | 画面视图 |
|---|---|---|
| 数据 | Herdr 观察流（差分帧、带光标） | 工作台按 pane 读取 `pane.read` 后只推送变化的行 |
| 远端尺寸 | 可申请尺寸控制 | 只读，从不申请尺寸控制，不改变 PTY |
| 手机阅读 | 宽任务需要缩小或平移 | 默认按本窗口宽度折行，字号可读；可切回“原样排版”横向滚动 |
| 选择复制 | 需进入“选择文本” | 直接长按或拖选，另有“复制画面文本” |
| 输入 | 直接输入终端或本地输入框 | 只用本地输入框；辅助键照常可用 |
| 不支持 | — | 光标位置、鼠标点击、图片；全屏程序只能按原排版查看 |

全屏程序（vim、htop、Agent 的确认菜单等）在画面视图中能看到当前画面，但无法折行重排，复杂交互请切回终端。

入口：终端工具栏的“画面视图”、手机终端角标菜单、切换面板的“操作”。画面视图头部的终端按钮切回终端；顶栏 Terminal / Chat 开关从画面视图切到对话，再切回时回到终端视图。

### 数据路径

1. 浏览器发送 `screen.watch {pane_id, lines}`，工作台回复 `screen.watching {gen}`；`lines` 默认 200，可分步增加到 500、1000。
2. 工作台读取 `pane.read`，固定 `source=recent_unwrapped`、`format=ansi`：
   - `recent_unwrapped` 按逻辑行返回，长行没有被远端宽度硬折断，浏览器才能按自己的宽度重排；
   - 只用 `ansi`：Herdr 只在 `format=text` 读取全屏 Agent 的历史时，才会向 PTY 注入滚轮事件抓取更早内容，`ansi` 没有这个副作用；
   - Herdr 单次最多返回 1000 行；全屏程序运行时只返回当前一屏。
3. Herdr 在每个带样式的行首重置 SGR，每行可以独立解析，所以工作台按行差分：先找出窗口向上滚动的行数（`drop`），再只发送内容变化的行（`set`）；变化超过八成时改发整帧。
4. 轮询节奏：画面变化时每 300 ms 读一次，静止后逐步放慢到 700 ms、1.5 s；本窗口发出输入后 80 ms 立即重读。页面隐藏、手机上切到其他 pane、断线时停止读取。
5. 同一 pane 的多个窗口共享读取：150 ms 内的结果或正在进行的读取会被复用；输入后的重读只接受输入之后开始的读取。
6. 每次重新订阅（改行数、重连）都换新的 `gen`，浏览器丢弃旧代次的帧，两个差分基准不会混用。

每个浏览器窗口最多同时订阅 4 个画面。画面视图只读，不打开新的观察流，也不参与尺寸控制。

## 在远程主机启动 Herdr

两条路径都遵守同一边界：**Herdr 必须独立于网站和受控端运行**。

- 网站和受控端只在 Herdr 没有运行时启动它；已在运行或状态不明的 Herdr 一律不动，不替换、不重启、不升级。
- 启动后的 Herdr 不能是网站请求、SSH 连接或 herdrx 服务的子进程组或 cgroup 成员。关闭网页、网站重启、herdrx 升级 / 重启 / 卸载都不能结束 Herdr、pane 或其中的任务。
- 网站不提供停止 Herdr 的入口。

### SSH 主机：网页按钮

工作台连不上 SSH 主机上的 Herdr 时（快照失败），连接遮罩显示“在远程主机启动 Herdr”；主机页“Herdr 能力”检查失败时也有同一按钮。只在用户点击时执行，Tailcat 主机不提供此按钮。

按钮通过 SSH 运行一段 POSIX sh：

1. 按 PATH、`~/.local/bin`、`/usr/local/bin` 查找 `herdr`；找不到时提示先安装。
2. `herdr [--session 名称] status server` 显示 running 时直接返回“已在运行”。
3. 用户已开启 linger 且用户 systemd 可用时，用 `systemd-run --user --scope --collect` 把 Herdr 放进独立 scope（单元名 `herdrx-herdr-<会话>-<时间戳>`），它不再依附任何登录会话；否则用 `setsid` 让 Herdr 进入新会话（与 Herdr 客户端自己拉起服务端的方式相同），macOS 等没有 setsid 的系统用 `nohup`。
4. 服务端经用户的登录 shell 启动（bash / zsh / sh / dash / ksh；其他 shell 改用 `/bin/sh`），pane 继承与终端登录一致的 PATH 和语言环境。
5. 最多等待 9 秒，确认 `status server` 为 running。

未开启 linger 时网页会提示：若系统在用户全部退出登录后清理进程，Herdr 可能被结束；可在远程主机执行 `sudo loginctl enable-linger <用户名>`。

### 受控端：服务启动时自动拉起

`herdrx serve`（`herdrx.service` / LaunchAgent）启动时，如果默认会话的 socket 不存在或拒绝连接，就拉起默认会话的 Herdr：

- Linux：用户 systemd 可用时，用 `systemd-run --user --scope` 放进独立 scope，脱离 `herdrx.service` 的 cgroup；`systemctl --user restart herdrx` 只结束 herdrx 自己的进程。
- 若 herdrx 运行在 systemd 服务里却无法创建独立 scope，就不拉起，并在日志说明原因：那样启动的 Herdr 会随 herdrx 服务一起被结束。
- macOS 和非 systemd 环境：以新会话启动，不在 herdrx 的进程组里（macOS 尚未实机验证）。
- 只拉起默认会话；命名会话不自动拉起。
- 设置 `HERDRX_HERDR_AUTOSTART=0` 关闭，例如 `systemctl --user edit herdrx` 加入 `Environment=HERDRX_HERDR_AUTOSTART=0`。

这项能力随 herdrx CLI 发布：需要 v0.1.0-rc.2 之后发布的 CLI 版本；更早的 CLI 不会自动拉起 Herdr。

## 验证记录

| 项目 | 方式 | 结果 |
|---|---|---|
| 差分算法 | Go 单测 + 2000 步随机序列往返 | 通过 |
| 画面推送、输入后重读、取消订阅、上限、错误、代次替换 | Go WebSocket 集成测试（`-race`） | 通过；输入后首帧 < 250 ms |
| ANSI 解析、颜色、差分应用、组件行为 | Vitest | 通过 |
| 手机折行、可读字号、只本地输入、不申请尺寸、旧代次丢弃、桌面入口、刷新保持 | `scripts/test-screen-view.mjs`，Chromium / Firefox / WebKit | 通过 |
| 真实链路 | herdrx-server + 隔离的 Herdr 0.9.1 会话（本机接入），390×844 手机视口 | 颜色、中文、长行折行正确；发送到回显约 207 ms，期间 2 帧共约 2.2 KB |
| 网页启动按钮 | herdrx-server 经真实 OpenSSH（System OpenSSH）连接本机、隔离的命名会话 | 遮罩出现按钮；启动后 Herdr 位于 `herdrx-herdr-<会话>-*.scope`，父进程为 1、独立会话；工作台自动重连；再次点击返回“已在运行” |
| 启动脚本 | Go 单测（假 herdr / loginctl）+ 真实运行 | setsid 路径进入新会话、不重复拉起；systemd 路径进入独立 scope，环境含登录 shell 的 PATH 和 LANG |
| 受控端自动拉起 | `herdrx serve` 作为临时 systemd 用户服务，隔离 XDG 目录 | Herdr 进入独立 scope；在其 pane 里运行任务后，`systemctl --user restart` 与 `stop` herdrx 服务，Herdr 与任务均存活，重启后不重复拉起 |

未验证：macOS 上的自动拉起与网页按钮（nohup 路径）、未开启 linger 时退出登录后的存活、`herdr update --handoff` 在 systemd scope 内的交接、真实手机安装后的画面视图手感。
