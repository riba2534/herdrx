# 连续滚动响应验收

日期：2026-09-06。基线为前一轮滚轮修复版本，候选版本增加控制通道复用和浏览器发送节奏优化。以下验证全部使用隔离数据、Herdr session 和合成全屏应用，没有向用户已有终端发送测试输入。

## 问题与改动

基线每批滚动都要读取 snapshot、启动 control 客户端、等待第一帧，再发送并释放；网页先等待 24 毫秒合并手势，还要等上一批 RPC 返回才发送下一批。这些等待叠加后，连续滚动会表现为停顿和成批跳动。

- 每条 Web 终端观察流按需创建滚动 controller，连续手势共用一个接入通道和来源布局，停手 250 毫秒后按序发送 release、排空输出并回收。
- 浏览器改用 `requestAnimationFrame` 合并同一帧内的手势；最多保留 8 个在途请求，按 WebSocket 顺序发送，无须每次等待网络往返。
- 未发送的手势有大小和时效限制；反向滚动丢弃旧方向的积压，停手超过 120 毫秒后不补发过时积压。换流、断连和失败后不重放结果不确定的输入。
- 每批手势合为一次管道写入；控制通道持续排空输出，页面仍使用原有观察流显示画面。
- 观察流关闭或访问取消立即取消对应 controller；已有 controller 占用时不抢占。显示字号和缩放仍不作为远程尺寸。

本轮性能测量采用 [前一轮接入方式](display-validation-2026-09-06.md)，仅将通道保留到一段连续滚动结束。后续[闪烁与手机手势修复](flicker-mobile-validation-2026-09-06.md)保留复用策略，但已用实际 PTY 网格和像素尺寸替代来源布局估算；本页数字是当时的性能结果。

## 浏览器与真实 Herdr 前后对比

`scripts/test-scroll-latency.mjs` 分别启动基线和候选网站、隔离 Herdr 及开启备用屏幕和 SGR 鼠标的应用，真实经过 HTTP 认证、WebSocket、Herdr 输入和 xterm 渲染。

每组发出 60 次三行滚动，以约 16 毫秒间隔派发 DOM wheel 事件，并记录对应终端文本变更。网络组在浏览器 WebSocket 的发送和接收方向各增加 60 毫秒固定延迟。

| 条件 | 版本 | 文本更新次数 | 响应中位数 | 响应 P95 | 最后一次输入至最终文本更新 |
|---|---|---:|---:|---:|---:|
| 同机，无附加网络延迟 | 基线 | 22 | 84.6 ms | 93.1 ms | 61.6 ms |
| 同机，无附加网络延迟 | 候选 | 40 | 25.5 ms | 42.6 ms | 29.6 ms |
| 模拟 120 ms 往返延迟 | 基线 | 4 | 431.6 ms | 593.6 ms | 593.6 ms |
| 模拟 120 ms 往返延迟 | 候选 | 35 | 156.4 ms | 165.8 ms | 135.9 ms |

这是该次隔离运行的结果，不能作为所有机器或网络的固定指标。计时终点为浏览器 DOM 中的终端文本更新，不含显示器扫描输出；合并到同一次更新的输入按最后一条对应输入计算延迟，因此同时报告更新次数和停止后的延迟。模拟网络只增加固定延迟，没有模拟丢包、带宽限制或真实 SSH/Tailcat 跨网传输。

## 其他验证

- Go 原生对照在同一隔离 Herdr 内分别运行每批重新接入和连续复用，各 30 次手势。最终运行中，应用接收输入的中位数从 33.83 ms 降至 0.11 ms；这是输入通道计时，不能替代上述浏览器显示计时。
- Chromium、Firefox、WebKit 的真实浏览器鼠标滚轮回归通过，覆盖全屏应用上下滚动、普通历史连续滚动、LF 列对齐，以及字号、缩放、横纵移动和移动布局。
- 49 项前端测试、TypeScript、lint 和构建通过。新增回归验证未收到应答仍可连续发送、在途数量受限、反向和过时积压丢弃、换流不继承旧手势。
- Go 普通全量、全量 race、vet 通过。新增测试覆盖单次 RPC 结束后通道可继续使用、空闲释放、关闭后不可重开、接入等待中的访问/请求取消、已有控制占用、无效手势和失败输入不重放。
- 真实 Herdr 验证任务 PID 与已建立的 94×39 网格保持，已有 controller 被拒绝抢占后仍可输入；浏览器关闭后，隔离应用继续接收验证输入。认证和管理浏览器回归通过。

## 复现

```bash
make web-build
go build -o /tmp/herdrx-scroll-candidate ./cmd/herdrx-server
HERDRX_TEST_HERDR=/path/to/herdr HERDRX_SCROLL_BENCH=1 \
  go test -count=1 -run '^TestScrollWithRealHerdr$' -v ./internal/herdr
HERDRX_TEST_HERDR=/path/to/herdr \
  node scripts/test-scroll-latency.mjs /path/to/baseline-herdrx-server /tmp/herdrx-scroll-candidate
HERDRX_TEST_ENGINES=chromium,firefox,webkit node scripts/test-workbench-display.mjs
```

原生与端到端对照需要显式提供真实 Herdr 二进制；性能对照还需要保留基线网站二进制。三引擎显示回归在 CI 中运行，真实 Herdr 性能对照尚未加入 CI。实体 iOS/Android、macOS Safari、真实跨网和长期运行仍按发版差距清单验收。
