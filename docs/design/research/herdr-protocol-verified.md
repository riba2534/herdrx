# 一手验证：herdr 0.8.2 远程连接与协议事实

> 以下全部为本机 herdr 0.8.2 源码阅读 + 实测（socket 直连 / CLI）得到的结论，非推测。

## 1. `herdr --remote` 到底是什么

```
本地终端 ──► 本地 herdr (thin client, 画 sidebar/tab bar, 处理 prefix key)
              │ bincode 帧 (u32 LE 长度前缀, bincode 2 standard/varint)
              ▼
         ssh <target> "$HOME/.local/bin/herdr remote-client-bridge [--session x]"
              │ (stdio 原样转发, 不解析)
              ▼
         远端 herdr remote-client-bridge ──UnixStream──► ~/.config/herdr/herdr-client.sock
                                                          (herdr server, 负责 PTY/ghostty-vt/检测/渲染)
```

- target 就是 OpenSSH 的目标（ssh alias / `ssh://user@host:port`），认证完全走 OpenSSH（公钥/agent/密码）。
- 远端必须有 herdr 二进制；本地 herdr 会自动检测/安装到 `~/.local/bin/herdr`。
- 远端 bridge 启动前会确保 server 在跑（`spawn_server_daemon`）并校验 `endpoint_protocol_generation == 1`。
- 远端 herdr 有两个 socket：
  - `~/.config/herdr/herdr.sock`：**JSON API socket**（NDJSON，一行一请求）
  - `~/.config/herdr/herdr-client.sock`：**客户端渲染协议 socket**（bincode 2，PROTOCOL_VERSION=22）
  - 命名会话：`~/.config/herdr/sessions/<name>/...`

## 2. 客户端渲染协议（client socket，bincode）

- `ClientMessage` / `ServerMessage` 两个大枚举，bincode 2 `config::standard()`（varint 整数、LE）。
- 两种客户端模式：
  1. **Direct terminal attach**（`TerminalHello`→`Welcome{encoding: TerminalAnsi}`）：服务端把**一个终端**渲染成 ANSI 差分字节流 `TerminalFrame{seq,width,height,full,bytes}` 推给客户端；客户端发 `Input{data}` 原始字节 + `Resize` + `AttachScroll`。
  2. **Client-owned shell**（`EndpointControl{kind:"endpoint.hello.v1", data: JSON}`，稳定契约 generation 1，承诺长期兼容）：服务端推
     - `ClientShellSnapshot`（JSON）：workspaces/tabs/panes/agents/commands(自定义命令)/focus/`server_keybindings_toml`
     - `PaneSurface(PaneSurfaceFrame)`：**整个活动 tab 的 pane 区域**渲染成 cell 网格 `FrameData{cells[{symbol,fg,bg,modifier,hyperlink}],width,height,cursor,hyperlinks}` + 每个 pane 的 rect/inner_rect/scroll + 可拖动 split 句柄 + popup
     - `PaneSurfacePatch`：增量 cell diff
     - 客户端自己画 sidebar / tab bar / modal，发 `ClientShellPaneInput{pane_id, events:[语义按键/鼠标/粘贴]}`、`ClientShellResize`、`ClientShellEndpointRequest{request: JSON}`（复用 JSON API 方法）
- 颜色编码：`0x00_00_00_XX` 命名色 / `0x01_00_00_XX` 256 色 / `0x02_RR_GG_BB` 真彩。

## 3. 纯 JSON 的第三方接入路径（官方文档明确写给 "third-party bridges"）

### 3.1 结构与控制：JSON API socket（NDJSON）
- 请求 `{"id":"...","method":"pane.split","params":{...}}` → 响应 `{"id":"...","result":{"type":"...",...}}` / `{"id":"...","error":{"code":"...","message":"..."}}`
- 91 个方法（schema 可用 `herdr api schema --json` 导出，本次已导出 255KB）：
  - session.snapshot（一次性全量：workspaces / tabs / panes / agents / layouts(每 tab 的 pane rect + splits) / focus / version / protocol）
  - workspace.* / tab.* / pane.*（split/swap/move/zoom/resize/focus_direction/rename/close/send_text/send_keys/send_input/read/scroll/process_info）
  - layout.export / layout.apply / layout.set_split_ratio（BSP 树：`{type:"split",direction:"right|down",ratio,first,second}`）
  - agent.list/get/read/explain/prompt/wait/send_keys/rename/focus/start/view.set
  - events.subscribe / events.wait / pane.wait_for_output
  - worktree.* / plugin.* / integration.* / notification.show / server.*
- 事件订阅（实测）：`{"method":"events.subscribe","params":{"subscriptions":[{"type":"pane.created"},{"type":"layout.updated"},...]}}`
  - 27 种订阅类型；`pane.agent_status_changed` / `pane.scroll_changed` / `pane.output_matched` 要带 `pane_id`
  - 响应 `{"result":{"type":"subscription_started"}}` 后，每行一条 `{"event":"pane_created","data":{...}}`
  - `layout_updated` 事件直接携带完整 layout（pane rect + splits[{id,direction,ratio,rect}]）
  - **注意**：订阅时服务端会回放一段历史事件（实测看到 30 分钟前的 workspace_created），客户端必须以 snapshot 为准、按 revision/幂等方式应用事件
- agent_status 枚举：`idle | working | blocked | done | unknown`（done = idle 且用户未看过）

### 3.2 终端内容：`herdr terminal session observe|control <pane|term|agent> --cols N --rows N`
- stdout：NDJSON `{"type":"terminal.frame","seq":1,"encoding":"ansi","width":100,"height":30,"full":true,"bytes":"<base64 ANSI>"}`，结束 `{"type":"terminal.closed","reason":...}`
- control 模式 stdin：
  - `{"type":"terminal.input","text":"ls\n"}` 或 `{"type":"terminal.input","bytes":"<base64>"}`
  - `{"type":"terminal.resize","cols":80,"rows":24}`
  - `{"type":"terminal.scroll","direction":"up|down","lines":3,"source":"wheel|page_key"}`
  - `{"type":"terminal.release"}`
- **所有权语义（实测 + 源码 headless.rs:1760）**：
  - observe：任意多个，只读，**帧是真实终端的裁剪窗口**（实测 60x12 观察 295x79 的 pane，得到左下角裁剪，不重排）
  - control：同一终端同一时刻只有一个 controller；接管时服务端 `direct_attach_resize_locks` 锁定该终端尺寸并把 PTY resize 成 controller 的 cols/rows → 桌面 TUI 会看到该 pane 内容按 Web 端尺寸重排；release 后解锁
  - `--takeover` 踢掉现有 controller

## 4. 对方案的直接推论
1. **传输统一用 SSH**：`herdr --remote` 本身就是 SSH；Go 侧用 `golang.org/x/crypto/ssh`，公网直连和 tailcat 隧道只是 `net.Conn` 来源不同。
2. **API socket 可以直接经 SSH 转发**：x/crypto/ssh 支持 `direct-streamlocal@openssh.com`，`client.Dial("unix", "~/.config/herdr/herdr.sock")` 即可拿到 NDJSON 通道，无需 socat。
3. **终端流用 SSH exec 通道跑 `herdr terminal session control`**：一个 SSH 连接可复用几十个 channel，每个 pane 一个。
4. **不需要在 Go 里实现 bincode**（阶段一）；未来若要"服务端渲染 cell 网格"再做 generation-1 稳定契约。
5. **Web 端每个 pane 一个 xterm.js**，ANSI 帧直接 `term.write()`；布局用 `layout_updated` 的 BSP/rect 在 CSS 里自己排，因此手机可以改成"单 pane + 滑动切换"而不受 TUI 平铺约束。
