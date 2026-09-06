# 滚动闪烁与手机手势验收

日期：2026-09-06。本次补充前一轮[滚动响应验收](scroll-performance-validation-2026-09-06.md)，分别检查终端帧绘制、手机触摸路由和真实 Herdr 滚动通道。浏览器检查使用本地隔离 HTTP/WebSocket 数据，不连接用户已有 pane。

## 绘制问题与修复

此前浏览器每收到标为 `full` 的帧，都会先同步调用 xterm `reset()`，再用异步 `write()` 解析 ANSI。两者之间的浏览器绘制可能读到空缓冲，造成滚轮期间反复闪灭。Herdr 的 `full` 表示完整单元格重绘；同一观察流中的后续重绘不要求重置整个终端。

在未修改 xterm 的隔离浏览器中，每隔 5–9 ms 写入一帧约 7 KB 的完整 ANSI，共 180 帧，通过 xterm 的 `onRender` 记录实际渲染结果：

| 引擎 | 原 `reset()` 后 `write()` 的空白渲染次数 | 重置与 ANSI 放入同一次 `write()` 后 |
|---|---:|---:|
| Chromium | 49 / 79 | 0 |
| Firefox | 46 / 81 | 0 |
| WebKit | 74 / 107 | 0 |

这是隔离连续输入的复现结果，不表示用户每次滚动都会产生相同次数的闪烁。

候选实现只在新观察流的第一帧前加入 RIS（`ESC c`），与该帧 ANSI 一起写入；后续 full 与 delta 帧保持原始顺序写入。首帧标志在接受写入时立即消费，避免等待解析回调期间到达的后续帧也被重置。历史快照同样在一次写入内完成重置与内容替换，并只对历史文本补齐 CRLF。

历史返回实时的验证还复现了独立问题：实时帧已完整进入缓冲并得到应答，画面却持续空白。xterm 重置期间暂时把内部行高设为零，历史视口残留的 DOM `scroll` 事件可能据此计算出 `NaN`，污染缓冲的显示行位置。候选实现拦截隐藏的 `.xterm-viewport` 原生滚动事件；历史仍通过 xterm 公开滚动 API 移动，放大画面的外层视口继续独立平移。

当前固定依赖 xterm 5.5 未实现 `?2026` 同步输出模式。其大帧解码分块使用同步循环，当前加载的插件没有引入异步 ANSI 解析器，因此单次完整写入可在该次解析中完成。下述超过 300 KB 的用例已验证这一行为；未来若引入异步解析插件，应重新检查帧原子性。

## 绘制验收结果

[`scripts/test-terminal-flicker.mjs`](../scripts/test-terminal-flicker.mjs) 加载实际构建后的 React 页面，经 WebSocket 二进制帧解码进入 xterm。测试按浏览器动画帧时间戳合并回调，保留该帧最后一个回调后的 DOM，避免把 xterm 绘制回调之前的临时 DOM 误判为空白。

| 覆盖 | 检查 | 结果 |
|---|---|---|
| Chromium、Firefox、WebKit；桌面和手机视口 | 持续约 7 KB full 帧 | 通过 |
| 同上 | 持续超过 300 KB 的合法彩色 ANSI full 帧 | 通过 |
| 同上 | 多帧排队，full 后紧接 delta | 通过 |
| 同上 | 进入普通历史，再返回权威实时首帧 | 通过 |
| 同上 | 空白行、混合帧代际、最终增量、协议应答、观察流数量及画面尺寸 | 通过 |

最终构建运行共 30 个场景、695 个动画帧样本，记录到 **0 个空白帧、0 个部分或混合帧**。普通绘制没有重开观察流，历史返回恰好建立一个替换观察流，发送的帧序号均得到应答，画面网格尺寸保持不变。

组件单元回归另以延迟解析回调验证：新流只重置首帧，排队的 full 和 delta 不重复重置；历史期间略过实时帧但仍应答；历史和实时切换均不调用同步 `reset()`。浏览器回归负责验证实际绘制效果，单元回归负责固定上述队列时序。

## 手机手势问题与修复

原触摸监听只阻止 xterm 接收 `touchstart` / `touchmove`，没有把纵向手势送入应用滚动入口。因此单指只能平移外层放大画面，画面贴合或到达边缘后无法继续浏览普通历史或全屏应用。前一轮手机回归仅检查横向平移，没有覆盖该路径。

[`terminalTouch.ts`](../web/src/lib/terminalTouch.ts) 现在按以下规则处理手势：

- 单指纵向移动超过 6 px 后锁定方向，先平移放大的画面，到边缘后只把剩余位移送往终端滚动入口。
- 保存分数平移目标，避免浏览器对 `scrollTop` 取整造成连续移动漂移。
- 横向平移、双指缩放、已有文本选择和轻点聚焦保留各自行为。
- 加入第二指、取消触摸、打开长按菜单、页面失焦或隐藏、观察流切换时，取消尚未发出的手势；第二指落在终端外的工具栏也会结束单指滚动周期。

新增回归曾复现同一事件任务中“单指移动后立即加入第二指”仍在后续动画帧多发一次 `terminal.scroll`；修复后的普通历史和全屏应用用例均没有重放该未发送手势。

## 手机验收结果与边界

[`terminalTouch.test.ts`](../web/src/lib/terminalTouch.test.ts) 的 9 个单元用例通过，覆盖方向、边缘、分数平移、原生手势保留、取消、选择、跨应用恢复和跨区域双指。

[`scripts/test-mobile-terminal-scroll.mjs`](../scripts/test-mobile-terminal-scroll.mjs) 在当前构建页面上的 Chromium 与 WebKit 检查通过：

| 环境 | 已执行检查 | 结果 |
|---|---|---|
| Chromium，CDP trusted touch | 实际 React/xterm 页面中的单指上下滑、普通历史和全屏应用、连续手势、正确单元格坐标、一次历史读取、键盘聚焦 | 通过 |
| Chromium，CDP trusted touch | 浏览器原生横向平移和双指缩放；包括单指拖动中途加入第二指，`visualViewport.scale > 1.05` | 通过 |
| Chromium / WebKit | 双指缩放取消尚未发送的纵向滚动；换流或手势失效后不重放；持续手势不重开流、不误发 raw input、resize 或 close | 通过 |
| WebKit，合成 TouchEvent | 触摸方向、历史与全屏路由、取消与输入聚焦 | 通过 |

以上浏览器均运行于 Linux。手机视口和触摸能力模拟不能替代实体设备；WebKit 的合成事件不触发操作系统原生手势，不能据此宣称 iPhone / iPad 真机手感、系统键盘或 macOS Safari 已验收。真实 iOS/Android、跨网 SSH/Tailcat 和长期运行仍需按发版差距清单验证。

## 真实 Herdr 滚动通道

隔离 Herdr 0.8.2 中同时运行原生 TUI、Web 观察流和 SGR 全屏应用，复现了第二个独立原因：API 的分屏矩形包括边框，67×49 的外框对应实际 65×47 PTY；CLI control 握手还会丢弃字符像素尺寸。按旧实现接入、释放各一次会触发两次 SIGWINCH，测试应用收到信号后清屏并重绘，网页因此收到真实的空白内容。只修复浏览器 `reset()` 无法消除这类源端空白。

候选实现读取 Herdr 主机上 pane 的实际 PTY 行列及像素尺寸，用限定版本的二进制协议建立原生滚动控制。全屏应用的鼠标模式、备用屏幕与主机历史路由仍由 Herdr 决定。连续手势继续复用通道，停手 250 ms 后有序释放，不抢占已有控制客户端，不重放结果不确定的输入。

[`scroll_flicker_integration_test.go`](../internal/herdr/scroll_flicker_integration_test.go) 使用独立目录、命名 session、模拟 8×16 px 字符的原生宿主终端及实际 Herdr TUI，等观察流和备用屏幕布局稳定后开始测量：

| 场景 | 实际 PTY 网格 | 实际像素尺寸 | 旧 CLI 接入，3 轮滚动新增 SIGWINCH | 候选产品通道，3 轮滚动新增 SIGWINCH |
|---|---|---|---:|---:|
| 原生客户端在线、单 pane | 134×49 | 1072×784 | 6 | 0 |
| 原生客户端在线、分屏 pane | 65×47 | 520×752 | 6 | 0 |

候选两场景的任务 PID、网格和像素尺寸均保持，手势到达应用。每场景观察到 10 个帧，其中 7 个 full；接入/释放仍会让 Herdr 请求完整重绘，由前述浏览器修复连续显示。已有真实 Herdr 集成测试也通过，覆盖上下滚动、空闲复用/释放、已有 controller 被拒绝抢占后仍可输入和任务存活。

本机、基础 SSH、Tailcat 的尺寸读取及 client socket 转发通过真实本地 SSH 和 PTY 测试：同一个 65×47、520×752 PTY 经三条路径读取得到一致结果且没有被改变。SSH 使用固定 Python 3 只读程序；Tailcat 使用配套 CLI 内建命令。配对前权限、认证后禁止原地升权、撤销断开存量 socket 并阻断新连接、binding epoch 失效均有真实本地 SSH 回归。这些是传输和授权路径验证，不是实际跨网测量。

支持范围与限制：

- 远程尺寸读取目前限 Linux；Python 3 是基础 SSH 全屏滚动的远程依赖，Tailcat 需更新配套 CLI。要求与错误排查见[安装说明](install.md#终端滚动兼容要求)。
- 协议 20 已通过真实 Herdr 0.8.2 验收，22 通过对应源码的独立报文样例测试；未核验的版本拒绝接入，不使用旧 CLI 回退。
- 无法精确还原的分数或饱和像素尺寸拒绝接入。读取 PTY 尺寸与控制握手之间，原生窗口若恰好并发改变大小，仍存在非原子时序边界；完全原子的保留尺寸操作需要 Herdr 提供相应 API。未将当前适配描述为绕过 Herdr 的尺寸控制。
- 字号、浏览器缩放和普通显示继续不改变远程尺寸。物理设备、真实跨网、并发原生窗口调整及长期运行仍属于后续专项验收。

## 最终检查

61 项前端单元测试、typecheck、lint、构建及三引擎显示/鼠标滚轮回归通过。Go 普通全量、全量 race、vet、真实 Herdr 集成、登录与管理浏览器回归通过。新增前端用例还验证 WebSocket 重连后即使 stream ID 重用，旧请求回复也不能影响新滚动队列。

连续响应对照保留前一轮优化：与最初每批重新接入的滚轮版本相比，60 次手势的同机 DOM 响应中位数为 80.6→30.7 ms；模拟 120 ms 往返延迟为 432.7→156.5 ms。另与上一轮已优化的版本比较，网络组为 158.8→158.7 ms，同机组为 30.0→37.5 ms；本次没有据此宣称进一步降低延迟。该对照仍是隔离运行和 DOM 文本计时，不能替代物理屏幕或实际跨网测量。

## 复现与持续回归

```bash
make web-build
pnpm --dir web exec playwright install --with-deps chromium firefox webkit
pnpm --dir web exec vitest run src/components/TerminalPane.test.tsx src/lib/terminalTouch.test.ts
node scripts/test-terminal-flicker.mjs
node scripts/test-mobile-terminal-scroll.mjs
HERDRX_TEST_HERDR=/path/to/herdr go test -count=1 -run 'TestScroll.*RealHerdr' -v ./internal/herdr
```

两项新增浏览器回归已加入 CI 浏览器步骤。绘制回归默认执行三引擎的桌面及手机视口，手机手势回归默认执行 Chromium 和 WebKit。真实 Herdr 通道回归需显式提供二进制，并始终创建独立隔离 session；尚未纳入 CI。
