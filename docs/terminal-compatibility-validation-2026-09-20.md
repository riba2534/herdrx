# Herdr 固定版本门禁与像素、ClientShell 实验

日期：2026-09-20。对应[实施方案](design/terminal-sizing-and-herdr-compatibility.md)的 A1、C2、D。实验使用隔离配置、命名会话和测试任务；没有连接用户会话，没有替换已安装的 Herdr。

## A1：固定二进制和不可跳过的真实实例测试

新增 [release 清单](../scripts/herdr-releases.json)记录固定 tag、资产名与 GitHub Release 的 SHA-256。下载地址只来自官方 `herdrdev/herdr`。缓存命中仍检查摘要，摘要错误直接失败，不执行缓存中的程序。

[执行器](../scripts/test-herdr-compatibility.py)先核对平台和二进制版本，再运行 `WithRealHerdr` 测试并解析 Go JSON 事件。它同时检查：

- 清单内每个必需测试确实执行且通过；删除、改名或空匹配均失败。
- 所有选中测试和子测试均不能 skip；进程退出码和 package 状态必须通过。
- 自动发现新增的同类测试，不将未来新增夹具遗漏在正则之外。
- 不继承宿主 `HERDR_*` socket/session 环境。所有夹具仍使用自己的临时配置与进程。

macOS 明确不选择 Linux 专用的 `TestScrollFlickerWithRealHerdr`，原因写入清单和输出摘要。其余适用测试必须执行；这不是把 skip 当作通过。门禁自身的六项回归覆盖成功、skip、遗漏测试、子场景 skip、包/进程失败和损坏缓存。

| 平台 | 固定版本 | 本轮本地执行 | CI 定义 |
|---|---|---|---|
| Linux amd64 | 0.8.2、0.9.0、0.9.1 | 三版各 8 个顶层测试、5 个子场景通过，均启用 race；零 skip | 三版全部必需 |
| Linux arm64 | 0.9.1 | 当前机器未执行 | 原生 arm64 runner，必需 |
| macOS amd64 | 0.9.1 | 当前机器未执行 | Intel runner，适用测试必需 |
| macOS arm64 | 0.9.1 | 当前机器未执行 | Apple Silicon runner，适用测试必需 |

六组真实 Herdr job 是候选 Docker 镜像构建的前置依赖，不再只有可选本地测试。工作流语法通过 actionlint 1.7.12。各 GitHub Actions 平台结果见 [PR #13](https://github.com/riba2534/herdrx/pull/13)，不能被本地 Linux 测试替代；真实跨网、macOS launchd 和实体手机验收仍单独记录。

浏览器整链门禁使用同一清单下载并校验 0.9.1：执行器的 `--download-only` 只在摘要和版本均匹配后输出二进制路径，交给 `test-terminal-control.mjs`。不会回退到 PATH 中未校验的 Herdr。

本地复现：

```sh
python3 scripts/test-herdr-compatibility-runner.py
python3 scripts/test-herdr-compatibility.py --version 0.9.1 --race \
  --cache /tmp/herdrx-test-releases --output /tmp/herdrx-test-results
```

清单只负责版本与平台覆盖，不授权升级任何远程主机。CLI/daemon 错配和能力降级由适配层测试另行验证。

### 浏览器显示回归

`test-workbench-display.mjs` 已迁移到显式 `terminal.control.acquire` 与带 epoch、generation、单调序号的 `terminal.resize_v2`。Chromium、Firefox、WebKit 全部通过原有显示回归，保留 320px 手机、横竖屏、40 次快速旋转、触摸滑动、辅助键盘和终端流不重建等检查。新增桌面/手机两组“手动释放、进入后台释放、回到前台不自动申请、刷新不恢复控制”的断言。

工具栏展开和收起必须保持 `.terminal-viewport` 的边界完全不变，所有按钮必须留在工具栏内；没有放宽原有断言。释放后保留最后远端网格，触摸滑动场景通过增大本地字号明确制造溢出，再验证真实触摸手势能滚动。

此脚本使用真实 React/xterm 与合成 HTTP/WebSocket 数据，验证布局和前端协议行为，不替代 `test-terminal-control.mjs` 的真实网站/Herdr 整链验证。

### CLI 观察画布固定问题与原生观察协议验证

真实整链验收发现初版方案的缺口：PTY 尺寸改变，并不意味着另一个 CLI observer 的帧尺寸随之改变。独立 0.9.1 实验中，observer 以 80×24 接入，control 把 PTY 调整为 60×20、随后 100×30；controller 输出新的 100×30 full frame，而 observer 后续帧始终为 80×24。向 observer 的 stdin 写 `terminal.resize` 仍无效。上游 `src/client/terminal_sessions.rs` 的 observe 命令没有 stdin 读取线程；服务端按每个客户端的 `terminal_size` 渲染画布。

因此只检查任务 ioctl 或只确认 `submitted` 不足以验收画面收敛。修正使用窄范围原生 `ObserveTerminal` 协议，在同一观察连接发送 `ClientResize`，只更新观察画布。它不发送控制、输入、focus 或 takeover；浏览器输入仍走已有 JSON 输入通道。

[真实观察协议回归](../internal/herdr/observe_integration_test.go)已加入固定版本必需清单。0.8.2（协议 20）、0.9.0 / 0.9.1（协议 22）分别实测以下三个场景，均通过 race：

- 没有控制客户端：观察画布变化不改变任务 PID、PTY 行列、像素和 SIGWINCH 计数。
- 外部 direct control 持续连接：任务保持 90×30、810×540 像素，观察画布变化及关闭不抢占控制。
- 原生 TUI 持续连接：任务保持 133×49、1064×784 像素，观察画布变化及关闭不引起额外 SIGWINCH。

每个场景在同一观察连接验证 70×23 → 50×17 → 130×45 → 90×30，四次均收到序号递增的 full frame；同时验证 observer 拒绝 stdin 输入。0.8.2 原生 TUI 像素测试显式开启该版的实验性 Kitty graphics，与已有滚动夹具一致，不以零像素代替像素保持验证。该测试在 macOS 矩阵中同样为必需；本机只执行了 Linux amd64，其他平台须以 CI 实际结果为准。

## C2：像素扩展继续禁用，有明确阻塞证据

### 浏览器度量

[像素实验](../scripts/probe-terminal-pixels.mjs)加载项目锁定的 xterm 5.5 DOM renderer，使用 Chromium、Firefox、WebKit，分别测量 DPR 1、1.25、2、3 与字号 10、13.37、14、18，共 48 组。每组固定 80×24 网格，等待字体与两次绘制，读取实际 `.xterm-screen` 和行元素尺寸；三引擎全部完成。

字号 14 的代表值如下，单位为像素。设备格宽是测得 CSS 格宽乘该浏览器实际 DPR 的值，不是已经认证的上游协议单位。

| 引擎 / DPR | CSS 格宽×格高 | 推导设备格宽×格高 |
|---|---|---|
| Chromium / 1 | 8.425×16 | 8.425×16 |
| Chromium / 2 | 8.425×16 | 16.85×32 |
| Firefox / 1.25 | 8.4375×约17.5833 | 10.546875×约21.9792 |
| WebKit / 3 | 8.425×16 | 25.275×48 |

48 组推导设备格宽均含小数，而上游 `cell_width_px/cell_height_px` 是整数。由屏幕总宽除列数获得的平均格宽，也不证明每一格可以按同一整数精确表示。因此本轮没有把 `CSS × DPR` 或四舍五入结果宣称为精确几何，没有向产品协议加入虚假的 8×16 默认精确值。

这验证的是 Linux 上浏览器模拟 DPR。没有把 CSS zoom 或合成 DPR 当作真实 browser zoom、Mac Retina、手机旋转或跨屏移动的完整验收。

### 上游 JSON control 的实际像素行为

[协议实验](../scripts/probe-herdr-protocols.py)使用官方 0.9.1，在同一测试任务中同时读取 `TIOCGWINSZ`、记录 SIGWINCH，并读取终端尺寸查询回复：

| 操作 | 真实列×行 | 像素 extent | 本步新增 SIGWINCH |
|---|---|---|---|
| 原生 shell 建立 8×16 字符格 | 119×40 | 952×640 | 基线 |
| CLI JSON control 以相同行列接入 | 119×40 | 0×0 | 1 |
| 补发 9×18 cell 的 `terminal.resize` | 119×40 | 1071×720 | 1 |
| 再次补发完全相同的 resize | 119×40 | 1071×720 | 0 |

补发后，`CSI 14 t` 回复 720×1071，`CSI 16 t` 回复 18×9，`CSI 18 t` 回复 40×119，与真实 ioctl 一致。任务 PID 保持不变。它证明整数像素补发可以工作，同时证明“先普通 attach，再补像素”有清零再恢复的两次变化；没有把额外 SIGWINCH 的体验成本认作已经可接受。

**采用决定：C2 不启用。** 先定义并验证 DOM 字符格整数映射、完整 browser zoom/实体设备行为，再验证本机、SSH、Tailcat 三条路径与初始 attach 时序。当前通过的本机实验不能替代尚未执行的后两条路径；JSON control 的 `pixel_mouse=false` 也仍限制语义像素鼠标。

## D：已实现限定 ClientShell 原型，暂不采用

同一协议实验包含一个仅供实验的 generation 1 编解码客户端，与原生 TUI 启动器不同：它直接连接隔离 daemon 的 client socket，明确控制是否发送 focus。该代码不进入网站或受控端适配器。

实验限定单主机、一个 tab、两个客户端，得到以下结果：

1. 成功协商 generation 1，以及 `shell.snapshot.v1`、`shell.surface.v1`、`shell.input.semantic.v1`、`shell.blob.v1`；读取实际 advertised methods/capabilities，包括 `surface_interest`、`surface_reuse`、`surface_delta`。
2. A 提供 140×50 surface，真实 pane 为 139×50；B 提供 50×25，先以 `surface_active=false` 接入，再激活但不发送 focus。A 已获焦时，B 激活后任务仍为 139×50。
3. B 仅发送中文 `TextCommit`，**没有发送 `Focus(true)`**，任务立即变成 49×25。中文确实到达原任务。这直接否定“只省略初始 focus 就能保持 herdrx 输入不抢尺寸语义”的方案。
4. B 的语义滚轮在原任务中收到对应 SGR wheel 字节；B 停用 surface 后不再接收 surface/patch，A 恢复尺寸控制。
5. 收到 full surface、pane patch 和 `endpoint.surface-delta.v1`。本原型检查消息头和传输，并未实现完整画面重建与逐字符视觉一致性验收。
6. B 重连后 daemon boot ID 和任务 PID 均不变。以 JSON `pane.send_text` 旁路输入，在本单 pane 场景保持 A 尺寸，但没有证明多 tab、focus/zoom 变化下混合两条通道的目标与视图一致。

**采用决定：不迁移产品到 ClientShell。** 直接语义输入已违反本项目“输入不自动获得尺寸控制”的核心规则；绕过输入通道的混合设计尚未证明一致性。原型没有覆盖图片、完整 surface 重建、跨 tab 迁移和性能对比，不声称通过迁移门槛。继续采用现有 observe/control 路径，不修改远程 Herdr 生命周期。

复现实验（`HERDR_BINARY` 指向已按 release 清单校验的 0.9.1 二进制）：

```sh
python3 scripts/probe-herdr-protocols.py --herdr "$HERDR_BINARY" \
  --output /tmp/herdrx-protocol-probes.json
node scripts/probe-terminal-pixels.mjs /tmp/herdrx-browser-pixels.json
```

两项脚本输出机器可读 JSON，且只在自己创建的临时目录内创建、停止测试 daemon。C2/D 的否决是本轮完成的实验结论，不代表相关能力已经交付。
