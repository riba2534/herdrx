# herdr 私有 wire 协议（bincode 2）与 client-owned shell 契约速查

> 来源：herdr-protocol 专项调研（herdr 0.8.2 源码，bincode 编码规则已用 Python 手工编码并与 wire.rs 冻结摘要逐条比对一致）。本文件只在未来决定实现 generation-1 bincode 客户端时才需要。

## 1. bincode 2 `config::standard()` 编码规则
- 无 magic / 版本头；小端；varint；无 limit。framing：`[u32 LE len][payload]`，读端先校验 `len <= max`（客户端读 2 MiB，开 kitty graphics 时 32 MiB；服务端读客户端 32 MiB），再要求 `consumed == len`。
- u8/i8：原样 1 字节。u16/u32/u64/usize：`<251` 1 字节；`≤65535` `0xFB`+u16LE；`≤u32::MAX` `0xFC`+u32LE；否则 `0xFD`+u64LE。i16/i32/i64：zigzag 后按无符号 varint。
- bool 0x00/0x01；char 按 u32 varint；String = usize varint 长度 + UTF-8；Vec = 长度 + 逐元素；Option = 0x00 或 0x01+T；struct/tuple 按声明顺序拼接；enum = 变体索引 u32 varint + 字段。`#[serde(default)]` 对 bincode 无影响。

## 2. 必须冻结的枚举顺序（wire.rs）
- ClientMessage：0 TerminalHello, 1 Input, 2 ClipboardImage, 3 Resize, 4 Detach, 5 AttachTerminal, 6 AttachScroll, 7 ObserveTerminal, 8 ControlTerminal, 9 GraphicsTransmissionResult, 10 GraphicsTransmissionStarted, 11 ClientShellHello（已废弃，服务端拒绝）, 12 ClientShellResize, 13 ClientShellPaneInput, 14 ClientShellPopupInput, 15 ClientShellEndpointRequest, 16 AttachMouse, 17 ClientShellHostTheme, 18 ClientShellFocus, 19 ClientShellMouseCapture, 20 EndpointControl。
- ServerMessage：0 Welcome, 1 Terminal, 2 Graphics, 3 ServerShutdown, 4 Notify, 5 Clipboard, 6 WindowTitle, 7 ReloadSoundConfig, 8 MouseCapture, 9 TerminalBell, 10 GraphicsFile, 11 GraphicsTransmissionRetired, 12 ClientShellSnapshot（二进制快照，现行客户端拒绝）, 13 PaneSurface, 14 SemanticNotification, 15 ClientShellError, 16 DirectTerminalKeyboardProtocol, 17 ClientShellKeyboardReportAll, 18 ClientShellEndpointResponseChunk, 19 PaneSurfacePatch, 20 EndpointControl。
- 嵌套枚举同样 append-closed：ClientPaneInputEvent（Key0/TextCommit1/Mouse2/Paste3）、ClientKeyCode（18 个变体，Char=15）、ClientKeyKind、ClientMouseButton、ClientMouseKind、ClientMousePosition、NotifyKind、SemanticNotificationKind/Sound、ClientShellPopupSize、PaneSurfaceSplitDirection、SurfaceGraphics{Target,Source,Format}、RenderEncoding 等。
- shell 客户端实际收发：发 20(hello)、12、13、14、15、18、19、4；收 20（welcome + `shell.snapshot.v1` JSON）、13、19、18、3，并应处理 5、6、8、14、15、17、7、9。

## 3. PaneSurfaceFrame / PaneSurfacePatch 增量语义
- `boot_id` 服务端进程标识；`projection_revision` 连接级快照修订（快照先于 surface 推送）；`surface_revision` 连接级单调递增，patch 携带 `base_surface_revision`。
- 服务端只在"安全"时发补丁：仅 PTY 内容脏、无 full redraw、无 popup、无 graphics、无 alt-screen 切换、补丁行不触碰超链接、内容修订稳定。否则全量。
- 客户端接受补丁的条件：boot/projection 相等、base == 当前 revision、patch.revision == 当前+1、无 popup/graphics、每个 pane 元数据与当前一致、每行落在某个 pane 的 inner_rect 或滚动条列内；任一失败静默丢弃等下一帧全量。
- 背压：每客户端 render 通道 1 个槽位，满了丢帧并标记下一帧强制全量；读慢只会漏中间帧。resize 后客户端 invalidate 并发 ClientShellResize，下一帧必为全量。

## 4. ClientShellEndpointRequest 复用 JSON API 的边界
- `request` 就是原样 JSON API 请求串；同一连接**同时只能有 1 个在途请求**，并发会被断开。响应按 512 KiB 分块 `ClientShellEndpointResponseChunk{boot_id, request_id, final_chunk, data}` 拼接后按 JSON API 响应解析；客户端超时 60s。
- 白名单 37 个方法：command.invoke, integration.install/list, layout.set_split_ratio, pane.close/copy_motion/copy_search/edit_scrollback/focus/focus_direction/input.set/link.activate/rename/resize/scroll/selection.read/split/swap/zoom, product_announcement.dismiss, release_notes.dismiss, server.reload_config, tab.close/create/focus/move/rename, workspace.close/create/focus/move/move_block/rename, worktree.create/list/open/remove。
- **不在此通道**：ping、server.stop、session.snapshot、pane.read、agent.*、events.subscribe。拓扑与 agent 状态只能靠 `shell.snapshot.v1` 推送；读屏幕文本、事件订阅仍必须走 `herdr.sock` JSON API。结论：即使实现 bincode 客户端，`herdr.sock` 路径也省不掉。
