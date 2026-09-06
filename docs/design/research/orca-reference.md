# Orca 终端 / SSH / 移动端实现调研报告（面向 herdr Web 客户端）

> 仓库：`../orca`（Electron 43 + React 19，版本 1.4.178-rc.2）。只读调研，所有路径相对仓库根。
> 每个主题按「Orca 怎么做 → 我们能否照搬 → 需要改造的点」组织。

---

## A. 终端渲染（xterm.js 集成）

### Orca 怎么做

**1. 库与版本（`package.json`）**

| 包 | 版本 | 备注 |
|---|---|---|
| `@xterm/xterm` | 6.1.0-beta.303 | 打了源码补丁（见下） |
| `@xterm/addon-webgl` | 0.20.0-beta.299 | 补丁：GlyphRenderer / TextureAtlas / WebglRenderer |
| `@xterm/addon-fit` | 0.12.0-beta.300 | |
| `@xterm/addon-search` | 0.17.0-beta.300 | |
| `@xterm/addon-unicode11` | 0.10.0-beta.300 | 主进程 headless 也加载，保证字宽一致 |
| `@xterm/addon-web-links` | 0.13.0-beta.300 | |
| `@xterm/addon-serialize` | 0.15.0-beta.300 | 前端 + 主进程共用，有补丁 |
| `@xterm/addon-ligatures` | 0.11.0-beta.300 | 手写补丁 |
| `@xterm/headless` | 6.1.0-beta.302 | **主进程 daemon 里镜像 pty 输出**，用于 scrollback 持久化 |

没有 canvas addon，没有 image addon，没有 ghostty-web。`pnpm-workspace.yaml` 的 `patchedDependencies` 列出 4 个 xterm 补丁；源码级补丁在 `config/patches/xterm-src/*.src.patch`，说明文档 `docs/reference/xterm-patch-regeneration.md:5-13`：xterm 主包补丁只改 4 个源文件（`CoreBrowserTerminal.ts`、`Types.ts`、`input/CompositionHelper.ts`、`common/SortedList.ts`），目的是 **IME composition 钩子 + 自定义 `xterm-composition-*` 事件 + SortedList 修复**。

**2. Terminal 构造与 addon 加载顺序**（`src/renderer/src/lib/pane-manager/pane-dom-creation.ts:41-56, 72-102, 132`）

```ts
const terminalOpts: ITerminalOptions = { ...buildDefaultTerminalOptions(), ...userOpts }
const terminal = new Terminal(terminalOpts)
installGuardedLinkProviderRegistration(terminal)   // 包住所有 link provider 的同步异常，防止 renderer 被 window.onerror 杀掉
installWindowsCtrlAltChordRepair(terminal)
const fitAddon = new FitAddon()
const searchAddon = new SearchAddon()
const unicode11Addon = new Unicode11Addon()
const webLinksAddon = new WebLinksAddon(onLinkClick, { hover: ..., leave: ... })  // hover 时自绘 tooltip div
serializeAddon: new SerializeAddon(),
webglAddon: null, ligaturesAddon: null   // WebGL 延后 attach
```

默认选项 `src/renderer/src/lib/pane-manager/pane-terminal-options.ts:31-75`：

```ts
allowProposedApi: true, cursorBlink: true, cursorStyle: 'block',
cursorInactiveStyle: 'outline'（仅 block 光标用 outline，bar/underline 失焦保持原样）,
fontSize: 14, fontWeight: '300', fontWeightBold: '500',
fontFamily: '"SF Mono", "Menlo", "Monaco", "Cascadia Mono", "Consolas", "DejaVu Sans Mono",
  "Liberation Mono", "Symbols Nerd Font Mono", "MesloLGS Nerd Font", "JetBrainsMono Nerd Font", "Hack Nerd Font", monospace',
scrollback: 5_000（DESKTOP_TERMINAL_SCROLLBACK_ROWS_DEFAULT）,
scrollSensitivity: 1.15, fastScrollSensitivity: 5,
allowTransparency: false,
minimumContrastRatio: 4.5（浅底）/ 3（深底，按背景亮度重新设置）,
macOptionIsMeta: false, macOptionClickForcesSelection: true,
drawBoldTextInBrightColors: true,
scrollbar: { width: 7 },          // VS Code 风格窄滚动条，FitAddon 会为它留 1 列
vtExtensions: { kittyKeyboard: true }   // 宣告 kitty 键盘协议
```

用户设置覆盖在 `src/renderer/src/components/terminal-pane/terminal-pane-manager-options.ts:130-150`：`terminalFontSize / terminalFontFamily / terminalScrollbackRows / terminalCursorBlink / terminalScrollSensitivity / terminalLineHeight / terminalWordSeparator / macOptionIsMeta`。scrollback 策略 `src/shared/terminal-scrollback-policy.ts:1-4`：默认 5000，范围 1000–50000，预设 `[5000,10000,25000,50000]`。

**3. WebGL 开关与降级**（`src/renderer/src/lib/pane-manager/pane-webgl-renderer.ts`）

- 三态设置 `terminalGpuAcceleration: 'on' | 'off' | 'auto'`（默认 auto，`pane-dom-creation.ts:120`）。`shouldUseTerminalWebgl` (`pane-webgl-renderer.ts:89-97`)：on 强开；auto 走 `getTerminalWebglAutoDecision()`。
- auto 决策 `terminal-webgl-auto-policy.ts:74-142`：非 Linux 一律允许；Linux Wayland 直接禁（issue #5319 输入卡死）；探测 `webgl2` context 拿不到则禁；拿不到 `WEBGL_debug_renderer_info` 则禁；renderer 名匹配 `/swiftshader|llvmpipe|softpipe|software rasterizer|virgl|svga3d/i` 软渲染则禁。
- addon **懒加载**：`terminal-webgl-addon-loader.ts:44` 用 `import('@xterm/addon-webgl')` 动态加载（243 KB 不进首屏 chunk），`main.tsx` 渲染完 React root 后立即 prime；加载失败最多重试 3 次（`:17`），失败的 pane 挂到 `panesAwaitingWebglAddon` 集合，加载成功后统一 attach+refit。
- attach 流程 `pane-webgl-renderer.ts:259-333`：先 `disposeWebgl` 保证单 addon 不变量 → `new WebglAddon()` → `addon.onContextLoss(...)` → `terminal.loadAddon(addon)` → `terminal.refresh(0, rows-1)`。构造抛异常时 auto 模式把全局 `suggestedRendererType='dom'`，后续新 pane 全走 DOM 渲染。
- **context loss 降级**：`pane.webglDisabledAfterContextLoss = true` 并 `disposeWebgl(pane, { refreshDimensions: true })`（下一帧 `safeFit` 重新量 DOM 渲染器的 cell 尺寸），`pane-webgl-context-loss-policy.ts:4-5`：60 秒内丢 3 次以上不再重试；恢复边界是 `resumePaneRendering`（tab 重新可见）或 GPU 设置变更。
- 释放 context 时主动 `getExtension('WEBGL_lose_context').loseContext()` 并把 canvas 置 0×0（`:162-176`），防 Windows/ANGLE 上快速切 tab 撞 Chromium 活跃 WebGL context 上限（#6874）。
- 后台 pane：`pane-rendering-control.ts:60-88` `suspendPaneRendering` 把 `webglAttachmentDeferred=true`、blur、停光标闪烁，然后 dispose WebGL（有一个"最近隐藏的 worktree 保留 live WebGL"的 retention 缓存 `terminal-webgl-hidden-retention.ts`）；恢复时 `resumePaneRendering` 重 attach。
- 附带修复：`repairPaneWebglCanvasDprMismatch`（隐藏时换显示器 DPR 变化）、`clearTextureAtlas`（TUI 快速重绘导致字形图集损坏但无 context loss 事件）。

**4. scrollback 持久化（"survives restarts"）**

不是靠前端 `serializeAddon` 存盘，而是 **主进程 pty daemon 内用 `@xterm/headless` 镜像每个 pty 的输出**，定期 checkpoint 到磁盘：

- `src/main/daemon/headless-emulator.ts:84-99`：`new Terminal({cols, rows, scrollback: 5000, allowProposedApi: true, logLevel: 'off', vtExtensions:{kittyKeyboard:true}})` + `SerializeAddon` + `Unicode11Addon`（注释：必须与 renderer 字宽测量一致，否则 emoji 行错位）。
- 快照结构 `src/main/daemon/terminal-snapshot.ts:5-25`：

```ts
export type TerminalSnapshot = {
  snapshotAnsi: string            // 当前屏（可能是 alt buffer）
  pendingEscapeTailAnsi?: string  // 半截转义序列尾巴（parser 里的，serialize 会丢）
  scrollbackAnsi: string          // alt 屏时单独保存的 normal buffer
  oscLinks?: TerminalOscLinkRange[]
  rehydrateSequences: string      // 模式重放（mouse mode、bracketed paste 等）
  frameRestoreAnsi?: string
  cwd: string | null; modes: TerminalModes; cols: number; rows: number
  scrollbackLines: number; lastTitle?: string; outputSequence?: number; terminalOwner?: TerminalOwner
}
```

- 落盘：`daemon-checkpoint-file.ts:9-28` 定义 `checkpoint.json`；配套增量 `output.log`（`terminal-history-log.ts`）。目录名 `encodeURIComponent(sessionId)`（`history-paths.ts:6`）。限制 `terminal-history-file-limits.ts:1-5`：log 最大 5 MB，checkpoint 最大 200 MB（超出裁最旧行）。
- 调度：`daemon-pty-runtime-state.ts:152` `CHECKPOINT_INTERVAL_MS = 5_000`，`daemon-pty-checkpoint-scheduler.ts:19-51` 仅在有 dirty session 时启动定时器；`daemon-pty-checkpoint-persistence.ts:9-80` 优先 `takePendingOutput` 追加增量，溢出或需要时才 `takeSnapshotAndCheckpoint` 全量。
- 前端恢复：`src/renderer/src/components/terminal-pane/terminal-snapshot-replay-paint.ts:81-146` 按顺序写 `[normalPrologue, scrollbackAnsi, altPrologue, altFrame]`，alt 屏时先在 normal buffer 重建历史再切 alt 画帧。
- 另有 shell 历史（bash/zsh `HISTFILE`）按 worktree 隔离：`src/relay/terminal-history.ts:22-102`，写到 `~/.orca-remote/terminal-history/<hash>-zsh_history`，通过 `HISTFILE` 与 `ORCA_HISTFILE` 双变量注入。

**5. 字体**：内置字体只有 `Geist-Variable.woff2`（UI）和 `SymbolsNerdFontMono-Regular.woff2`（终端 PUA 图标回退），见 `src/renderer/src/assets/fonts/`。用户字体前置 + 固定回退链 `layout-serialization.ts:36-65`（`buildFontFamily` 去重拼接，含 `"Orca Nerd Font Symbols"`）。**没有** `document.fonts.ready` 等待逻辑（grep 无结果）；字重固定 300/500。

**6. IME / 中文输入**

- 给 xterm 打了 composition 源码补丁（见上）。
- `terminal-ime-candidate-anchor.ts:13-37`：在 `compositionstart/compositionupdate` 后强制把 xterm 隐藏 textarea 的 `top/left/height/lineHeight` 同步到光标 cell 位置，让 OS 候选窗跟随光标（TUI 藏光标时 xterm 自己的定位会错）。cell 尺寸从 `.xterm-screen` 的 boundingRect / cols,rows 推算，不碰 `_core`。
- `lib/keyboard-layout/`：mac Option-as-Alt 探测（`detect-option-as-alt.ts`、`option-as-alt-probe.ts`）与输入源 ID；`macOptionIsMeta` 由此动态决定。
- e2e 有 `terminal-ime-native`（ibus 韩文）与 `terminal-ios-hangul-preedit-fixture.ts`。

**7. 剪贴板**

- 选中即复制是**设置项** `terminalClipboardOnSelect`（`terminal-pane-pane-links.ts:118-160`，`onSelectionChange` 触发；Linux 另有 PRIMARY selection 写入，100 ms 防抖）。
- 粘贴：`terminal-bracketed-paste.ts:24-25` 手动包 `ESC[200~ ... ESC[201~`，并把文本内 ESC 替换为 U+241B 防注入（`:50-60`）；`terminal-agent-paste-bracketing.ts:7-25`：即使没观察到 DECSET 2004，只要 pane 前台是已知 TUI agent（claude/codex 等）就强制 bracketed，防止多行粘贴的 CR 误提交。
- OSC 52（TUI 写剪贴板）默认开启，有一次性 toast 提醒（`osc52-clipboard-default-on-notice.ts:25-38`）。

**8. 链接**

- 激活手势 `terminal-link-activation.ts:3-34`：mac ⌘+click / 其它 ctrl+click 直接打开；**无修饰键左键单击**弹出 `TerminalLinkActionPopover`（复制 / 外部打开 / 内置浏览器打开，`TerminalLinkActionPopover.tsx:28-46`）。
- URL 跨行：`hard-wrapped-terminal-http-links.ts`、`edge-wrapped-terminal-http-links.ts` 做逻辑行 hit-test，`terminal-web-link-click.ts:48-50` 注释说 WebLinksAddon 只认物理行。
- 文件路径链接：自定义 `registerLinkProvider(createFilePathLinkProvider(...))`（`terminal-pane-pane-links.ts:83-86`）+ OSC 8 路由（`terminal-osc-link-routing.ts`）。
- hover tooltip 不用 xterm 默认，自绘 `.pane-link-tooltip` div 固定在 pane 角上（`pane-dom-creation.ts:61-65`）。

**9. 输出调度器（pty → xterm.write 之间）**（`src/renderer/src/lib/pane-manager/pane-terminal-output-*.ts`）

常量 `pane-terminal-output-queue-registry.ts:88-99`：

```ts
BACKGROUND_FLUSH_DELAY_MS = 50      BACKGROUND_DRAIN_INTERVAL_MS = 16
HIGH_PRIORITY_DRAIN_INTERVAL_MS = 4  BACKGROUND_CHUNK_CHARS = 16 * 1024
MAX_WRITES_PER_DRAIN = 2             HIGH_PRIORITY_MAX_WRITES_PER_DRAIN = 8
DRAIN_TIME_BUDGET_MS = 8             LARGE_BACKLOG_CHARS = 512 * 1024
MAX_BACKGROUND_QUEUE_CHUNKS = 4096
```

- 每个 terminal 一个队列 `queuedByTerminal`；前台（可见/聚焦）pane 高优先级，`drainQueuedOutputImpl`（`pane-terminal-output-drain.ts:73-100`）每轮先挑高优先级，再挑大积压（>512 KB），再 FIFO；每轮最多写 2（后台）/8（前台）次，时间预算 8 ms。
- 用 `terminal.write(data, cb)` 的回调作背压（"pacer-clocked"：前台 pane 等 xterm 确认 parse 完才放下一批，注释 `:64`）。
- 背压回传 pty：`terminal-delivery-credit.ts:20-56` 把主进程发来的每批数据的 credit 延后到所有消费者 parse 完再 ack；主进程侧 `src/relay/pty-source-credit-*.ts` 是配套的 credit 账本。
- 积压上限随 scrollback 缩放：`terminal-scrollback-policy.ts:33-42`，`max(2 MB, rows × 120 chars)`，超限用 warning 行替换旧 backlog。
- 有 `terminal-write-pipeline-health.ts` 检测 xterm 写管线"死亡"（write 回调长期不来）并触发重建。

**10. 核心文件清单**

| 文件 | 职责 |
|---|---|
| `lib/pane-manager/pane-dom-creation.ts` | `new Terminal` + addon 装配 + DOM 容器 |
| `lib/pane-manager/pane-terminal-options.ts` | 默认 ITerminalOptions |
| `lib/pane-manager/pane-webgl-renderer.ts` | WebGL attach / dispose / context loss |
| `lib/pane-manager/terminal-webgl-auto-policy.ts` | auto 模式 GPU 探测 |
| `lib/pane-manager/terminal-webgl-addon-loader.ts` | 动态 import addon-webgl |
| `lib/pane-manager/pane-rendering-control.ts` | 后台 pane 挂起/恢复 |
| `lib/pane-manager/pane-terminal-output-scheduler.ts` 及 `-drain/-writer/-queue-*` | 输出调度与背压 |
| `lib/pane-manager/terminal-ime-candidate-anchor.ts` | IME 候选窗跟随光标 |
| `lib/pane-manager/pane-lifecycle.ts` | `terminal.open()`、addon 挂载、dispose |
| `components/terminal-pane/TerminalPane.tsx` / `TerminalPaneSurface.tsx` | React 壳，宿主 pane-manager |
| `components/terminal-pane/terminal-pane-manager-options.ts` | 把 settings 转成 PaneManagerOptions |
| `components/terminal-pane/terminal-pane-pane-links.ts` | 链接 provider、选中复制 |
| `components/terminal-pane/terminal-bracketed-paste.ts` | 粘贴包裹与 ESC 消毒 |
| `components/terminal-pane/terminal-snapshot-replay-paint.ts` | 恢复快照回放顺序 |
| `main/daemon/headless-emulator.ts` | 主进程 headless xterm 镜像 |
| `main/daemon/daemon-pty-checkpoint-persistence.ts` | 5 s checkpoint 落盘 |
| `shared/terminal-scrollback-policy.ts` | scrollback 与 backlog 上限 |

### 我们能否照搬

- **xterm 6.x + fit/unicode11/web-links/search/serialize/webgl 这套组合可以直接照搬**，都是纯浏览器库，Web 版无障碍。
- **WebGL 三态设置 + 懒加载 + context loss 降级 DOM** 的策略可整体照搬；auto 探测里 Wayland 判断依赖 Electron 的 `window.api.platform`，Web 版可退化为只做 webgl2 探测 + 软渲染名单。
- **输出调度器思路**（前台高优先、`write` 回调背压、积压上限随 scrollback 缩放）非常值得借鉴，但 Orca 的实现有十几个文件且与其 credit 协议耦合，建议只抄策略与常量。
- 默认 ITerminalOptions（窄滚动条 7px、`minimumContrastRatio` 按背景亮度切换、`cursorInactiveStyle` 处理、`kittyKeyboard`）可直接用。

### 需要改造的点

- **scrollback 持久化**：Orca 靠主进程 daemon 里的 `@xterm/headless` 镜像，这一层在 herdr 里对应 **服务端**（herdr daemon）。Web 客户端刷新页面后要恢复历史，需要 herdr 服务端维护 headless 镜像或环形缓冲区并在 attach 时回放（可先做简化版：服务端保留最近 N MB 原始字节流，attach 时 `reset` + 回放；进阶再做 headless serialize 快照）。
- **补丁**：Orca 给 xterm 打了 IME 源码补丁，Web 版初期不建议维护补丁，用官方 `@xterm/xterm` 稳定版即可；IME 候选窗锚定逻辑（`terminal-ime-candidate-anchor.ts`）不依赖补丁，可抄。
- **剪贴板**：Web 版 `navigator.clipboard.writeText/readText` 需要安全上下文（HTTPS）和用户手势，PRIMARY selection 无法实现；OSC 52 在浏览器里只能走 `navigator.clipboard.writeText`。
- **链接打开**：Orca 的文件路径链接会调 IDE 打开，Web 版需替换为服务端能力或忽略；URL 用 `window.open`。
- **手机端**：见主题 E，移动浏览器上 WebGL 更易 context loss，建议默认 DOM 渲染或对移动 UA 走 `off`。

---

## B. 终端主题

### Orca 怎么做

**1. 数据结构极简**：`src/renderer/src/lib/terminal-themes/types.ts:1-3`

```ts
import type { ITheme } from '@xterm/xterm'
export type TerminalThemeMap = Record<string, ITheme>   // key 就是显示名
```

没有独立的 id / author / appearance 元数据，每个内置主题就是一条 xterm `ITheme`（`background, foreground, cursor, cursorAccent, selectionBackground, selectionForeground` + 16 个 ANSI 色），见 `defaults.ts:6-29`。目录合并 `index.ts:8-17` 用 `mergeTerminalThemeCatalogs`，重名直接 throw（`shared.ts:10-12`）。

**2. 内置主题清单（22 个）**

| 文件 | 主题 |
|---|---|
| `defaults.ts` | Ghostty Default Style Dark（默认深色）、Builtin Tango Light（默认浅色） |
| `classic.ts` | Tango Dark |
| `popular-dark-core.ts` | One Dark、Tokyo Night、Gruvbox Dark、Catppuccin Mocha、Solarized Dark |
| `popular-dark-extended.ts` | Material Dark、Ayu Dark、Rose Pine、Everforest Dark、Horizon Dark、Night Owl |
| `popular-light.ts` | Solarized Light、One Light、Catppuccin Latte、GitHub Light、Rose Pine Dawn、Gruvbox Light、Tokyo Night Light、Everforest Light |

**3. 没有 Cobalt2**（`grep -ri cobalt` 在 terminal-themes 与 settings 目录均无结果）。最接近的深蓝底是 Night Owl（`popular-dark-extended.ts`）：

```ts
'Night Owl': {
  background: '#011627', foreground: '#d6deeb', cursor: '#80a4c2', cursorAccent: '#011627',
  selectionBackground: '#1d3b53', selectionForeground: '#d6deeb',
  black: '#011627', red: '#ef5350', green: '#22da6e', yellow: '#addb67',
  blue: '#82aaff', magenta: '#c792ea', cyan: '#21c7a8', white: '#ffffff',
  brightBlack: '#575656', brightRed: '#ef5350', brightGreen: '#22da6e', brightYellow: '#ffeb95',
  brightBlue: '#82aaff', brightMagenta: '#c792ea', brightCyan: '#7fdbca', brightWhite: '#ffffff'
}
```

Tokyo Night（`popular-dark-core.ts`）：`background #1a1b26 / foreground #c0caf5 / selectionBackground #33467c / blue #7aa2f7 / magenta #bb9af7 / cyan #7dcfff / red #f7768e / green #9ece6a / yellow #e0af68 / brightBlack #414868`。
（scratchpad 里已有队友放的 `cobalt2.itermcolors`，Cobalt2 需要我们自己按 ITheme 22 键手写。）

**4. 主题切换实现**

- 设置字段 `src/shared/global-settings-types.ts:133-137`：`terminalThemeDark: string`、`terminalThemeLight: string`、`terminalUseSeparateLightTheme: boolean`、`terminalCustomThemes?: TerminalCustomTheme[]`，另有 `terminalDividerColorDark/Light`、`terminalColorOverrides`、`terminalBackgroundOpacity`、`terminalCursorOpacity`、`terminalPaddingX/Y`、`terminalInactivePaneOpacity`。
- 解析 `src/renderer/src/lib/terminal-theme.ts:111-142` `resolveEffectiveTerminalAppearance`：应用主题 `settings.theme ∈ system|dark|light` → 系统模式用 `matchMedia('(prefers-color-scheme: dark)')`（`:37-42`）→ 只有 light 且勾了 `terminalUseSeparateLightTheme` 才用 `terminalThemeLight`，否则**浅色 UI 里也用深色终端主题**（默认行为）。
- 合成 `terminal-appearance.ts:53-91` `composeActiveTerminalTheme`：基础主题之上叠 `overviewRulerBorder: 'transparent'`、滚动条滑块 `rgba(180,180,185,0.4/0.6/0.8)`、用户 `terminalColorOverrides`、背景/光标透明度（hex → rgba）。
- 应用到已存在实例 `terminal-appearance.ts:163-186`：遍历 pane，**值比较后才写 `pane.terminal.options.theme = theme`**（注释：写 theme 会重建调色板并丢掉 TUI 通过 OSC 4/10/11/12 改的颜色，所以无变化时跳过），同时按背景亮度重设 `minimumContrastRatio`（浅底 4.5，深底 3，`terminal-contrast-correction.ts:11-23`），设置 `allowTransparency`、`cursorStyle`、`cursorInactiveStyle`。**不重建 Terminal**。
- 应用 UI 主题：`lib/document-theme.ts:59-74` 在 `document.documentElement` 上 `classList.toggle('dark'/'light')`，切换瞬间加 `theme-transition-disabled` 类禁用所有 transition（`main.css:37-43`）避免各区域不同速淡入。Tailwind v4 用 `@custom-variant dark (&:is(.dark *))`（`main.css:28`）。
- 设置页有实时预览 `TerminalSettingsPreview.tsx:128-205`（独立 `new Terminal` 实例，`terminal.options.theme = composedTheme`）。

**5. 暗色终端嵌入浅色 UI**

- pane-manager 根容器背景直接设为终端主题 `background`：`terminal-appearance.ts:152` `paneBackground = theme?.background ?? '#000000'`，经 `manager.setPaneStyleOptions({ splitBackground: paneBackground, paneBackground, paddingX, paddingY, ... })`（`:233-243`）→ `pane-divider.ts:91-92` `root.style.background = splitBackground`。分割条颜色是单独设置项（`DEFAULT_TERMINAL_DIVIDER_DARK '#3f3f46'`、light `'#d4d4d8'`，`terminal-theme.ts:16-17`）。
- `terminal.css:39-47`：覆盖 xterm.css 把 `.xterm-viewport` 背景硬编码 `#000` 的规则改为 `transparent`，避免浅色主题在亚像素位置露出 1px 黑线。
- pane 标题栏文字颜色按终端背景亮度切换 `--terminal-pane-title-on-dark-*` / `-on-light-*` 两套 token（`main.css:87-104`）。
- `terminal-view-attributes-publisher.ts` 把合成后的主题推给主进程，供后台 pty 回答 OSC 10/11（前景/背景色查询）。

**6. 自定义主题导入**：`src/shared/terminal-custom-themes.ts:4-16`

```ts
export type TerminalCustomTheme = {
  id: string; name: string
  source: 'warp' | 'ghostty' | 'manual'
  mode: 'dark' | 'light' | 'unknown'
  terminal: TerminalColorOverrides   // 22 个色键 + bold
  importedAt: string; sourceLabel?: string; unsupportedFeatures?: string[]
}
```

上限 200 个（`:43`），选择值前缀 `custom:`（`:44`）。解析器在主进程：`src/main/warp-themes/parser.ts`（Warp YAML 主题，支持自动发现 `~/.warp/themes`）、`src/main/ghostty/theme-resolution.ts`（Ghostty 主题文件）。没有 iTerm `.itermcolors` / VS Code 导入。

### 我们能否照搬

- `Record<name, ITheme>` 的结构、`composeActiveTerminalTheme` 的叠加顺序、按背景亮度切 `minimumContrastRatio`、值比较后再写 `options.theme` 这几点可以原样照搬。
- 22 个内置主题色值可直接复制（都是 MIT 仓库内的常量）。
- `document-theme.ts` 的 dark/light class + 禁 transition 手法可直接用于我们的 Tailwind v4 项目。

### 需要改造的点

- **Cobalt2 需自建**：Orca 没有；按 ITheme 22 键手写一份，参考 scratchpad 的 `cobalt2.itermcolors`。
- 建议给主题加轻量元数据（`id`、`appearance`）而不是用显示名做 key，方便 URL/设置持久化与 i18n。
- 主题导入解析器（Warp/Ghostty）在主进程 Node 侧，Web 版若要支持导入需在浏览器端重写 YAML/Ghostty 解析（或先不做，只做 JSON 粘贴导入）。
- OSC 10/11 回复依赖主进程 headless 镜像知道当前主题，Web 版可由前端 xterm 自己应答（xterm 默认会），无需推送。

---

## E. 移动端（`mobile/` 目录）

### Orca 怎么做

**1. 技术栈**（`mobile/package.json`）：Expo SDK 55 + React Native 0.83 + React 19，路由 `expo-router`（`main: "expo-router/entry"`），`react-native-webview` 13.16、`react-native-gesture-handler`、`react-native-reanimated` 4、`expo-notifications`、`expo-secure-store`、`expo-clipboard`、`expo-haptics`、`expo-camera`（扫配对码）。终端相关依赖只有 `@xterm/xterm` 6.1.0-beta.303 + `@xterm/addon-unicode11` + `@xterm/addon-webgl`。`mobile/packages/expo-two-way-audio` 是语音听写（dictation）用的原生音频模块。独立 pnpm workspace，与桌面端不共享 node_modules（根 `pnpm-workspace.yaml` 注释说明）。

**2. 终端渲染 = WebView 内跑 xterm.js**

- `mobile/scripts/build-terminal-webview-engine.mjs:10-12`：postinstall 时用 esbuild 把 `@xterm/xterm + addon-unicode11 + addon-webgl` 打成一个 IIFE，目标 `chrome74`（兼容老 Android WebView），并内联 `WeakRef / structuredClone / replaceChildren` 的 polyfill（`:39-60`），生成 `terminal-webview-engine.generated.ts` 字符串后内联进 HTML。
- HTML 由 `mobile/src/terminal/terminal-webview-html/*.ts` 十几个模板字符串拼接（`document-shell.ts`、`terminal-init-and-write.ts`、`write-queue.ts`、`surface-touch-gestures.ts`、`selection-overlay.ts`、`terminal-fit-scale.ts`、`mouse-report-and-scroll-routing.ts`、`theme.ts` 等）。
- WebView 内 xterm 构造 `terminal-init-and-write.ts:52-73`：

```js
term = new Terminal({
  cols, rows, theme: terminalTheme, minimumContrastRatio,
  fontFamily: terminalFontFamily, fontSize: fontPxForScale(currentTextScale),  // BASE_FONT_PX = 13
  fontWeight: '300', fontWeightBold: '500', scrollback: 5000,
  disableStdin: false,            // 否则 xterm 不回 DA/DSR 查询
  cursorBlink: false, cursorStyle: 'bar', showCursorImmediately: true, cursorInactiveStyle: 'block',
  convertEol: false, allowProposedApi: true
});
term.open(surface); attachWebglAddon(true); loadAddon(new Unicode11Addon()); unicode.activeVersion = '11'
```

  WebGL 在 WebView 内也开，`terminal-webview-webgl-recovery-injected.ts:15-52`：context loss 后 dispose，**只延时 100 ms 重试一次**，再丢就永久 DOM 渲染；`visibilitychange` 回前台时重新 `applyTerminalTheme + clearTextureAtlas + refresh`（iOS 会保留 xterm 模型但丢 GPU 像素）。
- RN ↔ WebView 消息协议 `terminal-webview-messages.ts:4-28`（RN→WebView）：`ping | write | init{cols,rows,initialData,oscLinks,terminalTheme,fontScale,preserveScroll} | set-font-scale | resize | reflow | clear | measure | reset-zoom | cancel-select | do-select-all | set-theme`。WebView→RN 事件（`terminal-webview-contract.ts:47-64`）：`onSelectionMode / onSelectionCopy / onModesChanged(bracketedPaste, altScreen, mouseTrackingMode, sgrMouse) / onKeyboardAvoidanceMetrics(cursorY, contentBottomRow, rows, altScreen) / onHaptic / onTerminalInput(bytes) / onTerminalQueryReply / onTerminalTap / onFileTap / onOpenUrl / onTextScaleChange`。
- 输出分批：RN 侧 `terminal-write-coalescer.ts:1-7` 以 **48 ms 窗口（约 20 Hz）**合并 postMessage（"busy PTY ~200 帧/s，per-postMessage 走 WebKit IPC 太贵"），空闲时首块立即发（leading edge）；上限 512 K UTF-16 单元。WebView 内再有 `write-queue.ts` 队列，`init()` 完成前的写入排队，`web-ready` 前 RN 侧也排队（`terminal-output-streaming-findings.md` 的结论：**WebView 装好 message handler 前 postMessage 会被丢**）。
- `terminal-fit-scale.ts:37-41`：**不按视口改 rows**（`adjustRowsForViewport` 是刻意的 no-op），而是让 PTY 保持服务端尺寸，WebView 里用 CSS `transform: scale()` 把整块终端**缩放到适配手机宽度**（fit-to-width），双指缩放改 `userScale`。cold-start 等 cell 宽度可测量最多重试 60 帧。

**3. 软键盘与输入**

- 两种模式（`mobile-terminal-direct-input-default.md`）：**buffered**（可见文本框，回车整体发送）与 **direct**（隐藏 RN `TextInput` 捕获按键字节直接发 pty）。2026 起默认 direct（每个 terminal handle 一次性默认，用户可切回）。输入不经 xterm 的 textarea：WebView 里 `mousedown/click` 都 `preventDefault`（`surface-touch-gestures.ts:69-70`），焦点由原生 TextInput 持有。
- 虚拟按键条 `terminal-key-definitions.ts:97-143`：Esc、Tab、Enter、Shift+Tab（`ESC[Z`）、Space、⌫（`\x7f`，可长按重复）、Del、↑↓←→（可重复）、Ctrl+C/D/L/Z/R/A/E/W/U。可自定义快捷键（`TerminalShortcutSettings.tsx`、`CustomKeyModal.tsx`）：`buildTerminalShortcutKey({key, modifiers: ctrl|alt|shift})` 生成字节，规则在 `terminal-accessory-keys.ts:68-170`：ctrl+字母 → 控制字节表、alt → `ESC` 前缀、方向/Home/End/F1-4 → `CSI 1;N final`（无修饰 F1-4 用 SS3 `ESC O P`），Ins/Del/PgUp/PgDn/F5-12 → `CSI n;N ~`。
- 硬件键盘特殊键映射 `terminal-live-input.ts:36-63`（RN `onKeyPress` 的 key 名 → 字节）；Enter 留给 `onSubmitEditing` 防止双发。
- 键盘避让 `terminal-keyboard-avoidance-lift.ts:9-29`：不用 KeyboardAvoidingView 整体上推，而是按 WebView 报的 `cursorY / contentBottomRow` 算出最小上推量（只把光标行推到键盘上方，alt 屏则整体上推）。
- 键盘类型固定 `default`（`terminal-keyboard-type.ts:4-10`，ASCII 键盘会隐藏中文等 IME）。IME 预编辑有 `terminal-live-preedit-mirror.ts` 镜像显示。

**4. 手势**（`surface-touch-gestures.ts:44-140`）：单指纵向滑动 → 驱动 xterm buffer 滚动（带速度融合与惯性 momentum），横向仅在内容比视口宽时平移；双指 → 缩放 `userScale`（松手吸附到字号预设并通过 `onTextScaleChange` 持久化）；长按/拖拽选择由自绘 `selection-overlay.ts`（带把手的选择层，`seedWordSelection` 按词选中）+ 顶部 Copy / Select All 按钮。TUI 开了鼠标模式（`mouseTrackingMode !== 'none'`）时，滑动改为发送滚轮鼠标报告或方向键（`mouse-report-and-scroll-routing.ts:32-60`，`buildArrowScrollSequence` 根据 `applicationCursorKeysMode` 选 `ESC[`/`ESC O`）。有 haptic 反馈（edge-bump）。

**5. 横竖屏 / 字号 / 主题**：`terminal-viewport-refit.ts:46-70` 监听 `useWindowDimensions`、tab 条显隐、面板宽度、textScale 变化后重新 `measure` 并调 `terminal.updateViewport` RPC 让 **服务端 pty resize 到手机 cols/rows**（有 debounce 与 capability 判断）。字号预设 `TERMINAL_TEXT_SCALES`（base 13 px）。默认主题 `terminal-webview-html/theme.ts:4-27` 是 Tokyo Night 色值，主题由桌面端通过 `RuntimeMobileTerminalTheme` 推给手机（`set-theme` 消息），与桌面端终端主题一致；app UI 是固定深色 graphite（`mobile-theme.ts`）。

**6. 通知**：桌面端产生 `agent-task-complete | terminal-bell` 事件，通过 RPC 流订阅推到手机后用 **`expo-notifications` 本地通知**展示（`mobile-notifications.ts:38-70`，`notification-routing.ts:5-18`）；重连后按 watermark seq 追赶（#8129），无 APNs/FCM 服务端推送。

**7. 与桌面端连接**（`mobile/src/transport/`）

- 桌面 Electron 主进程起 **WebSocket RPC 服务，端口 6768**（`README.md`，`host-endpoint.ts:21`），配对走 Settings > Mobile 的 QR 码（`use-mobile-install-qr.ts`，含 deviceToken + 服务端公钥）。
- 直连 `direct-rpc-client.ts`：请求/流注册表、重连计划（`RPC_RECONNECT_ATTEMPT_LIMIT`，原来 12 次约 6.5 min 放弃）、liveness watchdog。
- **E2EE**：`e2ee.ts:1-4` tweetnacl Curve25519 ECDH + XSalsa20-Poly1305，JSON RPC 走 `base64([24B nonce][ciphertext])` 文本帧，终端流走原始字节；v2 用 HKDF-SHA256 派生双向 key + sessionId（`mobile-e2ee-v2-key-schedule.ts:7-32`）。relay（见 F）看不到明文。
- LAN 直连不可达时走 cloud relay，可达时自动"直连升级"（`mobile-relay-direct-upgrade.ts`、`mobile-endpoint-supervisor.ts` 带 hysteresis）。
- **issue-5049 经验**（`issue-5049-unresponsive-session-findings.md`）：① 重连次数耗尽后永久停摆，且没有 AppState 监听，回前台不重连 → 加 `notifyForeground()`：connected 时立即探活、reconnecting 时清退避重置计数立刻重连；② Android 后台杀 TCP 不发 `onclose`，`readyState` 仍 OPEN，半开连接要 20 s 间隔 + 8 s 超时才发现 → 回前台立即探活；③ `forceReconnect` 换了 client 但 hook 只在 ref 为 null 时读取 → 屏幕一直用已关闭的旧 client；④ 网络切换（Wi-Fi→蜂窝）也要触发 revive。

**8. 移动端能做什么**：查看/驱动终端、新建 worktree/agent、看 diff 并评论、看文件、PR 侧栏、native chat、语音听写、浏览器 screencast。桌面端有 **mobile presence lock**（`lib/pane-manager/mobile-driver-state.ts:4-10`）：手机在驱动某 pty 时桌面 xterm 丢弃 `onData/onResize` 并显示"Take back"横幅，避免两端同时 resize 打架。

**9. 桌面 `components/mobile/`** 是配对页（QR、网络接口选择、Windows 防火墙提示、Android 安装帮助），不是终端。

### 我们能否照搬

- **虚拟按键条的键表与字节生成器**（`terminal-key-definitions.ts` + `buildTerminalShortcutKey`）可以原样搬到 Web 版手机布局，纯 TS 无 RN 依赖。
- **48 ms 写合并 + leading edge** 的思路适用于我们 WebSocket → xterm.write 的路径（在浏览器里不需要 postMessage，但合并仍能减少重绘）。
- **键盘避让按光标行计算**、**TUI 鼠标模式下滑动转滚轮报告/方向键**、**双指缩放吸附预设**、**visibilitychange 后 clearTextureAtlas + refresh** 都可直接照搬到 PWA。
- **前台恢复三条教训**（回前台立即探活、重连计数重置、半开连接探测）对 Web 版手机浏览器同样成立（iOS Safari 后台会冻结 WebSocket）。

### 需要改造的点

- Orca 手机端是 **fit-to-width CSS 缩放 + 服务端 pty 用桌面尺寸**（或 updateViewport 到手机尺寸），我们是纯 Web，建议直接 fit 到手机 cols/rows 并 resize pty，同时保留 pinch 缩放字号。
- 输入不能像 RN 那样用原生 TextInput 绕开 xterm；Web 版直接用 xterm 自带 textarea，但要处理 iOS Safari 软键盘弹出时 `visualViewport` 变化（Orca 没有这部分经验）。
- E2EE + relay 只在需要穿透 NAT 时才需要；herdr 若走 HTTPS/WSS 反向代理可省略。
- 通知：Web 版只能用 Web Push（需 Service Worker + VAPID），Orca 的本地通知方案不适用。

---

## G. 快捷键系统与终端按键冲突

### Orca 怎么做

**1. 数据结构**（`src/shared/keybindings/types.ts`）

- `KeybindingActionId` 是字符串字面量联合（`:27-117`），共 **88 个命令**（`definitions-core-1..4.ts`），分组 `scope: 'global' | 'tabs' | 'terminal' | 'browser' | 'editor' | 'fileExplorer' | 'composer' | 'settings'`（`:3-11`），匹配上下文 `KeybindingContext = 'app' | 'terminal' | 'browser'`（`:13`）。
- 定义示例 `definitions-core-1.ts:7-14`：

```ts
{
  id: 'worktree.quickOpen', title: 'Go to File', group: 'Global', scope: 'global',
  searchKeywords: ['shortcut', 'global', 'file', 'quick open'],
  defaultBindings: platformBindings(['Mod+P'])          // 或 { darwin: [...], linux: [...], win32: [...] }
  // 可选：conflictGroup: 'menu'，allowInTerminal: true，allowBareKeybindings，allowShiftOnlyKeybindings
}
```

- 按键字符串格式：`Mod+Shift+BracketRight`、`Ctrl+Tab`、`Alt+1`、`Mod+Comma`；`Mod` 按平台映射 ⌘/Ctrl；键名用 `code` 风格（`BracketLeft`、`Comma`、`ArrowUp`），`parser.ts:19-60` 有别名表（`[`→`BracketLeft`、`ESC`→`Escape`、`PGUP`→`PageUp`…）。还支持 **修饰键双击**绑定（`DoubleTap:Control` 之类，`matching.ts:28-38`，`modifier-double-tap-detector.ts:5` 窗口 300 ms）。
- 用户覆盖 `KeybindingOverrides = Partial<Record<ActionId, string[]>>`（`types.ts:119`），存 **`~/.orca/keybindings.json`**（`main/keybindings/keybinding-file.ts:27-29`），文件含 `keybindings` 公共段 + `darwin/linux/win32` 平台段（`:56-65`），冲突项读入时剔除并生成 diagnostics。旧版存在 settings 里的 `keybindings` 字段一次性迁移（`keybinding-service.ts:37-39`）。

**2. 默认表（终端/tab 相关部分）**

| 命令 | mac | linux/win |
|---|---|---|
| tab.newTerminal | Mod+T | 同 |
| tab.close / terminal.closePane | Mod+W | 同 |
| tab.reopenClosed | Mod+Shift+T | 同 |
| tab.nextAllTypes / previousAllTypes | Mod+Shift+] / [ | 同 |
| tab.nextSameType / previousSameType | Mod+Alt+] / [ | 同 |
| tab.previousRecent | Ctrl+Tab（allowInTerminal） | 同 |
| tab.nextTerminal / previousTerminal | Ctrl+PageDown / PageUp（allowInTerminal） | 同 |
| tab.selectByIndex | Ctrl+1..9 | Alt+1..9 |
| terminal.copySelection | Mod+C | Ctrl+Shift+C, Ctrl+C |
| terminal.paste | Mod+V | Ctrl+V, Ctrl+Shift+V, Shift+Insert |
| terminal.selectAll | Mod+A | Ctrl+Shift+A |
| terminal.search | Mod+F | 同 |
| terminal.clear | Mod+K | 同 |
| terminal.focusNextPane / PreviousPane | Mod+] / Mod+[ | 同 |
| terminal.expandPane（zoom） | Mod+Shift+Enter | 同 |
| terminal.splitRight | Mod+D | Mod+Shift+D |
| terminal.splitDown | Mod+Shift+D | Alt+Shift+D |
| sidebar.left.toggle | Mod+B | 同 |
| worktree.quickOpen | Mod+P | 同 |
| worktree.palette | Mod+J | Mod+Shift+J |

**3. 注册与分发**

- Electron 主进程在 `webContents` 的 **`before-input-event`** 里先解析（`src/main/window/main-window-shortcut-routing.ts`、`main-window-shortcut-actions.ts`），走 `src/shared/window-shortcut-policy.ts:170+` 的 `resolveWindowShortcutAction(input, platform, overrides, {context, terminalShortcutPolicy})` 返回 `WindowShortcutAction` 联合（zoom / openSettings / toggleLeftSidebar / switchRecentTab / jumpToTabIndex …），再 IPC 到 renderer 执行。`main-window-focus-lifecycle.ts:61` 注释："before-input-event 在 renderer keydown 之前解析快捷键，因此把 xterm 焦点镜像到主进程，让 Terminal-first 能让 shell 拿到应用组合键"。
- renderer 侧另有 `terminal-workspace-keydown.ts`（终端工作区 keydown）、`RecentTabSwitcher.tsx`，编辑器 / 文件树各自 capture 阶段监听（`editor-shortcuts.ts:28`、`useFileExplorerKeys.ts:291`）。没有统一的 command registry，动作分发是 switch 到 store action。

**4. 终端内冲突处理（ctrl+b 等）**

- 用户级开关 `terminalShortcutPolicy: 'orca-first' | 'terminal-first'`（`types.ts:17`，设置 UI `ShortcutTerminalPolicyControl.tsx:55-66` "Shortcuts in Terminal: Orca first / Terminal first"）。
- 判定 `effective.ts:76-96`：

```ts
export function isKeybindingAllowedInTerminal(d) { return d.scope === 'terminal' || d.allowInTerminal === true }
export function keybindingIsActiveInContext(d, options) {
  if (options.context !== 'terminal') return true
  if (policy === 'orca-first') return true          // 应用吃掉所有绑定的组合键
  return isKeybindingAllowedInTerminal(d)            // terminal-first：只有 terminal scope 或 allowInTerminal 的才生效
}
```

  即：**默认 orca-first 时，Mod+B（切侧栏）在终端聚焦时也会被应用吃掉**；用户切到 terminal-first 后，只有 `scope:'terminal'` 的命令与显式 `allowInTerminal: true` 的（Ctrl+Tab、Ctrl+PgUp/PgDn、workspace.delete、dashboard.toggle）仍被应用拦截，其余（含 Ctrl+B）透传给 pty。没有 tmux 式 prefix/leader key 机制。
- 与 xterm 的协作 `xterm-bypass-policy.ts:9-19`：xterm 的 kitty 键盘编码器会把所有带修饰键的组合（包括 Cmd+C）转成 CSI-u 并 `preventDefault`，导致浏览器原生 `copy` 事件不触发。所以 `attachCustomKeyEventHandler`（`terminal-pane-pane-input.ts:95`）里对以下情况**返回 false 让 xterm 不处理**：
  - `shouldBypassXtermKeyboardEvent`（`:287-340`）：① 事件已 `defaultPrevented` 且按着平台主修饰键（窗口级快捷键已处理，别再发给 shell）；② Shift+非 ASCII 单字符（让布局文本经 keypress 出来）；③ mac 上 Mod+C / Mod+V；④ Win/Linux 上 Ctrl+Shift+C、Ctrl+Shift+V、Ctrl+V、Shift+Insert，Ctrl+C **仅在有选区时**旁路（否则是 SIGINT 必须到 shell）。
  - `shouldHandleTerminalInterruptKeyboardEvent`（`:255-268`）：纯 Ctrl+C 直接 `terminal.input('\x03')` 绕过 kitty 编码器；`isTerminalInterruptCKey` 用 `code === 'KeyC'` 兜底非拉丁布局（俄语/韩语键盘）。
  - 大量 IME 守卫（`shouldSuppressTerminalImeKeyboardEvent`，keyCode 229、composing、Linux 候选词数字键）。
  - 纯修饰键 keydown/keyup（Alt/Control/Meta/Shift）不给 xterm（`:277-279`），防 kitty 协议下发送裸修饰键事件。

**5. 用户可配置性**：设置页 `ShortcutsPane.tsx` + `ShortcutRecorderButton.tsx`（录制按键）+ `ShortcutRemoveButton` + `KeybindingsFileActions.tsx`（打开/重置 keybindings.json）+ 冲突检测（`findKeybindingConflicts`、`conflictGroup`）+ 搜索。文件变更由主进程 `KeybindingService` 读取并广播快照（`keybinding-service.ts`）。

**6. 键盘布局**：`lib/keyboard-layout/` 解决 mac 上 Option 是 Alt 还是组合字符键（`detect-option-as-alt.ts`、`option-as-alt-probe.ts`）、非拉丁布局下用 `code` 推回基础字符（`layout-base-character.ts`）；`native/keyboard-layout-macos` 是 Swift 小工具读系统当前输入源。

**7. Electron Menu vs DOM**：菜单只挂了极少 accelerator（`app-menu-selection-item.ts:16` Command+C/A、`register-app-menu.ts:188` CmdOrCtrl+V），主要走 before-input-event；**web 模式**（`orca serve`）没有主进程拦截，renderer 用 `terminal-workspace-keydown.ts` 等 DOM 监听兜底，注释 `xterm-bypass-policy.ts:314-316`："Web clients still need paste to bubble to Chromium's native paste event"。

**8. 浏览器保留键**：Electron 下无此问题；web 模式下未看到对 Ctrl+W/Ctrl+T/F5 的特殊规避（Mod+W 关 tab 在浏览器里会被浏览器吃掉，Orca 没处理）。

### 我们能否照搬

- **`shared/keybindings` 整个纯 TS 包**（types / parser / normalization / matching / effective / formatting）无 Electron 依赖，可以直接复制进 Web 项目作为快捷键引擎。
- `terminalShortcutPolicy` 二选一 + `allowInTerminal` 白名单的模型简单可靠，建议照搬。
- `xterm-bypass-policy.ts` 的 clipboard 旁路规则与纯 Ctrl+C 处理可直接照搬（Web 版更需要，因为没有 Electron 菜单兜底）。

### 需要改造的点

- 拦截点从 `before-input-event` 改为 **`window.addEventListener('keydown', ..., {capture: true})`**，并在 xterm 的 `attachCustomKeyEventHandler` 里检查 `event.defaultPrevented`（Orca 已有这个分支）。
- 浏览器保留组合键（Ctrl+W/T/N、Ctrl+Shift+N、F5、Ctrl+L）无法可靠拦截，默认表要避开：Orca 的 Mod+W 关 tab / Mod+T 新终端在 Web 里需换（如 Alt+W / Alt+T）或只在 PWA 安装态启用。
- **ctrl+b 建议**：若 herdr Web 想给 tmux 用户透传 ctrl+b，直接采用 terminal-first 默认值，或者让 herdr 自己的 leader 用 `Ctrl+B` 之外的键（Orca 没有 leader 机制，需要自建）。
- keybindings.json 改为 localStorage / 服务端用户设置。

---

## C. 分屏（splits）与 resize 节流

### Orca 怎么做

**1. 布局树数据结构**（`src/shared/terminal-tab-types.ts:59-88`）

```ts
export type TerminalPaneLayoutNode =
  | { type: 'leaf'; leafId: string }                       // leafId 是稳定 UUID，用于 paneKey / 持久化
  | { type: 'split'; direction: 'vertical' | 'horizontal'
      first: TerminalPaneLayoutNode; second: TerminalPaneLayoutNode
      ratio?: number }                                      // first 的 flex 比例 0–1，缺省 0.5
export type TerminalLayoutSnapshot = {
  root: TerminalPaneLayoutNode | null
  activeLeafId: string | null
  expandedLeafId: string | null                             // zoom 状态也持久化
  ptyIdsByLeafId?: Record<string,string>; buffersByLeafId?: Record<string,string>
  scrollbackRefsByLeafId?: Record<string,string>; titlesByLeafId?: Record<string,string>
}
```

严格二叉树（每个 split 只有 first/second），同方向连续分屏也是嵌套而非扁平数组。tab 组层还有一套同构的 `TabGroupLayoutNode`（`tab-types.ts:8-17`）。注意 `direction: 'vertical'` 在 Orca 里指 **左右排列**（`flexDirection: 'row'`，`pane-tree-ops.ts:271`），与 tmux 语义相反。

**2. 关键设计：pane-manager 是手写 DOM，不是 React 渲染**。`PaneManager`（`lib/pane-manager/pane-manager.ts:73-125`）持有 `root: HTMLElement` 与 `Map<number, ManagedPaneInternal>`，split/close 直接操作 DOM：

- `wrapInSplit`（`pane-tree-ops.ts:249-296`）：新建 `div.pane-split.is-vertical|is-horizontal`（`display:flex`），用它替换原 pane 节点，再依次 append `[existing][divider][new]`，两个子元素 `flex: '1 1 0%'`（或按 ratio `${ratio} 1 0%` / `${1-ratio} 1 0%`）。
- `promoteSibling`（`:191-218`）：关闭 pane 后把兄弟节点提升到祖父位置，继承父级 flex；祖父是 root 时改成 `width/height: 100%`。
- 树 ↔ 持久化：`layout-serialization.ts:74-126` 遍历 DOM class 生成 `TerminalPaneLayoutNode`，ratio 从 `style.flex` 的 grow 值反推，与 0.5 差 <0.005 时不存。
- React 只负责壳（`TerminalPaneSurface.tsx`）和 overlay（通过 portal 挂到 pane 容器里）。好处：分屏/重排不会触发 React 重挂载 xterm（xterm 重建代价高且会丢 WebGL context）。

**3. 分割条拖动**（`pane-divider-drag.ts`）

- `MIN_PANE_SIZE = 50`px（`:15`），小布局下退化为 `min(50, total/2)`（`:242-246`）。
- pointerdown：`setPointerCapture` + 同时在 window 上挂 capture 阶段的 pointermove/up/cancel/blur（Chromium 可能瞬时丢 capture，`:97-103`）；WSLg 下 press 是 mouse、move 是 pen，所以允许"同为非触摸的 primary pointer"接管（`:216-227`）。
- pointermove：只改两侧 `style.flex`，且用 **rAF 合并**（`createDividerFlexFrameScheduler`，每帧最多一次写，`:18-63`）。
- **拖动期间不向 pty 发 resize**：pointerdown 时 `holdPtyResizesForPaneSubtrees([prevEl, nextEl])`（`:207-209`，注释："shells redraw prompts on every SIGWINCH；拖动中仍本地 fit xterm，只在 drop 时转发最终尺寸"）。`pane-pty-resize-hold.ts:30-61` 用 WeakMap<paneEl, {depth, pending}> 记录被压住的最新 cols/rows，释放时 `dispatchEvent(CustomEvent 'orca-pane-pty-resize-hold-flush')`，pty-connection 侧监听后再 `transport.resize`（`pty-input-forward.ts:212-227`）。
- pointerup 才 `refitPanesUnder(prev/next)` + flush hold + `onLayoutChanged`（持久化）。
- 双击分割条 → 两侧 `flex: 1 1 0%` 恢复均分（`:256-269`）。
- 分割线视觉：元素本体透明宽 hit 区，可见线用 `::after` 画，颜色 `--orca-terminal-divider-color`，粗细可设（`terminal.css:73-90`）。

**4. 焦点管理**

- `setActivePane(paneId, {focus})`（`pane-manager.ts`）：更新 `activePaneId`、按 `inactivePaneOpacity/activePaneOpacity` 调整各 pane 透明度（`applyPaneOpacity`），默认 `terminal.focus()`，变化时回调 `onActivePaneChange`。
- 点击聚焦：容器 `pointerdown` → `shouldFocusTerminalFromPanePointerDown` 排除 pane 内的 input/textarea/button/contenteditable（标题编辑器等 portal 进来的控件，`pane-pointer-focus.ts:1-21`）。
- focus-follows-mouse 是可选项（`focus-follows-mouse.ts:18-40`）：任何鼠标键按下时不切（避免打断选择/拖动）、窗口无焦点时不切。
- 失焦光标：`cursorInactiveStyle` 见主题 A。

**5. 键盘导航**：`terminal.focusNextPane / focusPreviousPane`（Mod+] / Mod+[）按 `getPanes()` 顺序循环，`terminal.equalizePaneSizes`（默认未绑定）、`terminal.expandPane`（Mod+Shift+Enter）；pane 之间还能**拖拽重排**（`pane-drag-reorder.ts`，pane 左上有 `.pane-drag-handle`，支持 DropZone 到另一 pane 的四边或拖出到 tab 组）。没有按方向（上下左右）导航的命令。

**6. zoom / expand**（`components/terminal-pane/expand-collapse.ts:39-84`）：不改树，从目标 pane 向上遍历到 root，把每层的兄弟节点 `display: none`、路径节点 `flex: 1 1 auto`，并把原 `display/flex` 存进 `Map<HTMLElement, {display, flex}>` 快照；恢复时回写快照再 `safeFit`。注释强调不要清空 split 容器的 inline `display:flex`（否则 FitAddon 量不到尺寸）。`expandedLeafId` 随布局持久化。

**7. resize → pty 全链路**

1. 每个 pane 的 `xtermContainer` 挂 **ResizeObserver**（`pane-fit-resize-observer.ts:139-151`）→ `requestStablePaneFit`。
2. **稳定帧检测**（`:84-136`）：连续 rAF 读 `fitAddon.proposeDimensions()`，直到两帧结果相同或与当前 cols/rows 相同，最多 8 帧（`MAX_STABILITY_FRAMES`）；注释：Windows 右侧栏打开时会出现 1 列宽的滚动条抖动，不等稳定会让 Codex 收到 SIGWINCH 风暴"发抖"。
3. `safeFit`（`pane-fit.ts:87-130`）：不可测量（display:none / 0 尺寸）直接返回并登记 continuation 等可见时重试；尺寸未变则跳过（"divider drags often stay within one cell"）；变了才 `fitAddon.fit()`，前后保存/恢复滚动位置（pinned viewport marker）。
4. xterm `onResize` → `forwardPtyResize`（`pty-input-forward.ts:200-233`）：若 mobile 正在驱动此 pty 则不发；若处于拖动 hold 则只记录 pending；否则 `transport.resize(cols, rows, {claim:true})`（IPC 到主进程 → node-pty / 远端 relay）。
5. **尺寸再断言**（`createPtySizeReassertion`，`:236-256`）：resize 是 fire-and-forget，reveal 后读 `pty.getSize` 与 xterm 比对，只在漂移时补发；恢复回放后显式 `SIGWINCH`（POSIX 只在尺寸变化时才发）。
6. 多终端共享 pty（桌面 + 手机 / 两台桌面）时用 `mobile-fit-overrides.ts` 的 `FitOverride {mode, cols, rows}` 让非 owner 端把 xterm 钉在 owner 尺寸，避免 resize 大战（`pane-fit.ts:110-124`）。
7. 隐藏 tab 的 pane：`suspendPaneRendering` 后 fit 被 defer（`pane-fit-continuation-registry.ts`），reveal 时统一 `fitRevealedPanes`。

**8. 移动端覆盖**：`mobile-fit-overrides.ts:1-60` 三种 hold 模式 `'mobile-fit' | 'remote-desktop-fit' | 'desktop-fit'`，按 ptyId 存 override，变化时通知 TerminalPane 显示"手机正在控制"横幅并触发 safeFit。

**9. 核心文件清单**

| 文件 | 职责 |
|---|---|
| `lib/pane-manager/pane-manager.ts` | 门面类：split/close/setActivePane/style |
| `lib/pane-manager/pane-manager-types.ts` | ManagedPane / PaneManagerOptions / PaneStyleOptions |
| `lib/pane-manager/pane-tree-ops.ts` | wrapInSplit / promoteSibling / refitPanesUnder |
| `lib/pane-manager/pane-split-close.ts` | splitManagedPane / closeManagedPane |
| `lib/pane-manager/pane-divider.ts` + `pane-divider-drag.ts` | 分割条 DOM、拖动、双击均分 |
| `lib/pane-manager/pane-pty-resize-hold.ts` | 拖动期间压住 pty resize |
| `lib/pane-manager/pane-fit.ts` + `pane-fit-resize-observer.ts` | safeFit、稳定帧、ResizeObserver |
| `lib/pane-manager/pane-drag-reorder.ts` + `pane-drag-pointer.ts` | pane 拖拽重排 |
| `lib/pane-manager/focus-follows-mouse.ts` + `pane-pointer-focus.ts` | 焦点规则 |
| `lib/pane-manager/mobile-fit-overrides.ts` | 多端共享 pty 的尺寸仲裁 |
| `components/terminal-pane/layout-serialization.ts` | DOM ↔ TerminalPaneLayoutNode |
| `components/terminal-pane/expand-collapse.ts` | zoom |
| `components/terminal-pane/pty-connection/pty-input-forward.ts` | onResize → transport.resize |
| `shared/terminal-tab-types.ts` | 布局树与快照类型 |

### 我们能否照搬

- **布局树类型 + ratio 语义 + 序列化规则**可直接照搬。
- **"拖动中只 fit 本地 xterm、drop 时才发 pty resize" + "ResizeObserver → 稳定帧 → fit → onResize → 发 pty"** 的链路是精华，强烈建议照搬（避免 TUI 重绘风暴）。
- 双击均分、MIN_PANE_SIZE 50、rAF 合并 flex 写入、分割线 ::after 画法都可直接用。
- zoom 的"隐藏兄弟 + 快照恢复"实现简单可靠。

### 需要改造的点

- Orca 用手写 DOM 管理 pane 是为了绕开 React 重挂载；我们若用 React，可以用 **稳定 key + 绝对定位/CSS grid** 的方式让 xterm DOM 节点在树变换中不被卸载（或同样把 pane 容器交给一个非 React 的 manager，React 只渲染壳）。二选一，但不要让 xterm 的宿主 div 随 React 树结构变化重建。
- Orca 没有按方向导航（h/j/k/l）和 tmux 式 `resize-pane` 键盘命令，若 herdr 需要要自建（可基于 DOM rect 做几何最近邻）。
- `transport.resize` 在 Web 版就是 WebSocket 消息；再断言逻辑需要服务端提供 `getSize` 查询。
- 手机上通常单 pane，分屏树可以保留但 UI 退化为 tab 切换。

---

## D. SSH 远程

### Orca 怎么做

**1. 总体架构：ssh2 直连 + 在远端部署 Orca 自己的 relay 守护进程**

- 本地主进程用 `ssh2`（`package.json` `^1.17.0`，`pnpm-workspace.yaml` 里 `ssh2: false` 表示不编译 `cpu-features` 原生模块）建立一条 `SshConnection`（每个 target 一条，`ssh-connection-manager.ts:9-60` 按 `target.id` 去重、并发 connect 抢占用 Symbol 标识）。
- 首次连接时通过 SFTP/`exec` 把 **relay bundle**（`src/relay/` 打包产物，含 node-pty）上传到远端 `~/.orca-remote/<version>/`（`relay-protocol.ts:31` `RELAY_REMOTE_DIR = '.orca-remote'`，版本化目录 + 安装锁 + GC，`ssh-relay-deploy.ts`、`ssh-relay-versioned-install.ts`），远端需有 Node（`ssh-remote-node-resolution.ts`）；Linux 上 node-pty 可能需要现场编译（`ssh-relay-build-toolchain.ts`，docs 给出 apt/dnf/apk 安装提示）。
- 启动 relay：`conn.exec(launchCmd)`，等 stdout 出现哨兵 `ORCA-RELAY v0.1.0 READY\n`（`relay-protocol.ts:28-30`，超时 10 s），随后 **ssh 通道的 stdin/stdout 就是 JSON-RPC 传输**。帧格式：13 字节头 + JSON（"matching VS Code's PersistentProtocol wire format"，`relay-protocol.ts:1-3`），`MessageType.Regular=1 / KeepAlive=9`，应用层 keepalive 5 s、超时 20 s（`:39-41`）。
- 一条 ssh 连接上跑 `SshChannelMultiplexer`（`ssh-channel-multiplexer.ts`）复用 pty / 文件系统 / git / agent hook 等所有 RPC，请求超时 30 s（`:59`）；**不为每个终端开新 ssh channel**。
- 远端 relay 同时监听一个 Unix socket（`~/.orca-remote/...`，`relay-socket-path-limit.ts`），供远端 `orca` CLI / agent hook 回连；socket 用 32 字节 base64url 凭据文件（0600）鉴权（`ssh-relay-endpoint-credential.ts:7-13`）。
- 需要 ProxyJump / ProxyCommand / FIDO2 security key / GSSAPI / ControlMaster 时**退回系统 OpenSSH**：`ssh-transport-selection.ts:71-91`，`system-ssh-command.ts:25-50` spawn `ssh` 子进程，用其 stdin/stdout 作为同样的 relay 传输。

**2. 连接配置与认证**（`ssh-connection-utils.ts:186-232`）

```ts
const config = {
  host: effectiveHost, port: effectivePort, username: effectiveUser,
  readyTimeout: CONNECT_TIMEOUT_MS /* 30_000 */, keepaliveInterval: 15_000, tryKeyboard: true
}
if (agent) config.agent = agent                      // SSH_AUTH_SOCK / Windows openssh-ssh-agent pipe / IdentityAgent
if (agent && resolved?.forwardAgent) config.agentForward = true
configurePrivateKeyAuthentication(config, keys, encryptedKeyPath)   // 未加密显式 key 优先，加密 key 延后到 agent 失败后走 passphrase 提示
```

- 解析 `~/.ssh/config`：不自己重实现，而是跑 **`ssh -G <host>`** 拿最终生效值（`ssh-g-config-resolution.ts`，含 `Include`、`Match`、`HostKeyAlias`、`IdentityAgent`、`ProxyJump` 等），另有 `ssh-config-parser.ts` / `ssh-config-include-expander.ts` 做导入时的 Host 列表。`SshTarget`（`shared/ssh-types.ts:12-52`）字段：`configHost, host, port, username, identityFile, identityAgent, identitiesOnly, gssapiAuthentication, proxyCommand, jumpHost, source: 'ssh-config'|'manual', relayGracePeriodSeconds, lastRequiredPassphrase, portForwards`。
- 默认 key 探测 `ssh-auth-resolution.ts:12-45`：`~/.ssh/id_ed25519, id_rsa, id_ecdsa, id_dsa, id_xmss`（sk 类走系统 ssh）。
- 密码 / passphrase：`ssh-connection.ts:733-985` `requestCredential('passphrase'|'password')` 回调 renderer 弹框，`SSH_CREDENTIAL_TIMEOUT_MS = 120_000`；passphrase 内存缓存到应用退出，可选更长 TTL。
- **known_hosts**（`docs/reference/ssh-host-key-verification.md`，`ssh-host-key-verifier.ts`）：读取 `ssh -G` 报告的 `UserKnownHostsFile/GlobalKnownHostsFile`（不可用则 `~/.ssh/known_hosts`+`known_hosts2`）作为信任源，**只读不写**；自己的信任记录写到 Orca 私有 store（`ssh-host-key-store.ts`）；默认 TOFU（首次接受并记住并显示指纹），`StrictHostKeyChecking yes` 时拒绝未知主机；key 变化/类型变化在密码提示之前拒绝，并给出 `ssh-keygen -R` 命令。文档坦承此前版本 `hostVerifier` 直接 `return true`（全接受）——这是后补的安全修复。

**3. 断线重连**

- 状态机 `SshConnectionStatus`（`ssh-types.ts:160-168`）：`disconnected | connecting | auth-failed | deploying-relay | connected | reconnecting | reconnection-failed | error`；renderer 侧 `ssh-connection-recoverability.ts:6-36` 用两张全量 Record 判定"正在连"与"可重连"。
- 退避表 `RECONNECT_BACKOFF_MS = [1000, 2000, 5000, 5000, 10000, 10000, 10000, 30000, 30000]`（`ssh-connection-utils.ts:35`），`SshReconnectLadder`（`ssh-reconnect-ladder.ts:32-78`）：连接稳定 ≥60 s 后重置阶梯；只有**握手失败**才累计 `consecutiveFailedAttempts`，9 次后 `give-up`（进入 `reconnection-failed`，等用户点 Connect）；纯 flap（连上又掉）不累计，且延迟被 `FLAP_DELAY_CAP_MS` 压住，保证在远端 relay 最短 grace（60 s）内重连回来。
- 错误分类 `ssh-reconnect-error-classification.ts`：把 OpenSSH 文本错误映射为 transient / definite-host-failure / auth / host-key。
- **远端 pty 存活**：relay 在远端持有 pty，本地断开后进入 grace（默认 0 = 永不过期，可设 60 s – 7 天，`ssh-types.ts:5-9`）；重连后 `pty.attach` 带 `resume: {ownerGeneration, ownerLease}` 和 `outputFlowControl` capability（`relay/ssh-pty-open-client-request.ts:11-49`）重新接管（"lease"），relay 回放最近 100 KB 输出（`RecentPtyOutputBuffer`，见 `docs/reference/ssh-reconnect-source-recovery.md`），全屏 TUI 从已渲染帧恢复。关闭桌面 app 也不杀远端 pty（docs "Sessions across app close"）。
- UI：`TerminalSshReconnectOverlay.tsx:41-76` 覆盖在终端上，按状态给文案 + Connect 按钮；连接按钮有 UI 级超时 20 s / 重连 180 s（`ssh-connect-ui-timeout.ts:6-12`，后台继续连）；侧栏 worktree 卡片有绿/黄/红状态 chip 与 Connect 控件。

**4. 端口转发**（`ssh-port-forward.ts`）：`SshPortForwardManager` 有两个 provider——`Ssh2PortForwardProvider`（`conn.forwardOut` + 本地 `net.createServer`）与 `SystemSshPortForwardProvider`（系统 ssh `-L`）；远端用 `/proc/net/tcp` 扫描监听端口（`ssh-port-scanner.ts`）显示在 Ports 侧栏，一键转发；转发持久化在 target 上并随重连恢复；特权端口自动映射（80 → 10080）。

**5. 数据流与背压**：远端 pty → relay（`src/relay/relay-pty-source-*.ts`，credit 账本 `pty-source-credit-*.ts`）→ 13 字节帧 → ssh channel → 本地 multiplexer → `ssh-pty-consumer-session.ts` → 主进程 pty 路由 → renderer 输出调度器。消费端按 `requestedWindowSu` 申请窗口、逐批 ack（`ssh-pty-source-credit-adapter.ts`），主进程 `pty-source-credit-scheduler.ts` 负责发放 credit；renderer parse 完才 ack（见主题 A 第 9 点）。

**6. 多路复用**：每 target 一条 ssh 连接 + 一个 relay 进程，所有终端/文件/git 走同一 multiplexer；系统 ssh 路径下另用 OpenSSH `ControlMaster`（`ssh-control-socket.ts`，"Reuse SSH connection for faster setup" 默认开）。

**7. 核心文件清单**

| 文件 | 职责 |
|---|---|
| `main/ssh/ssh-connection.ts` | ssh2 Client 生命周期、认证、hostVerifier、重连 |
| `main/ssh/ssh-connection-utils.ts` | connect config、常量、shell 转义 |
| `main/ssh/ssh-connection-manager.ts` | 每 target 单连接池 |
| `main/ssh/ssh-transport-selection.ts` | ssh2 vs 系统 ssh 的选择 |
| `main/ssh/ssh-auth-resolution.ts` | agent / 默认 key 解析 |
| `main/ssh/ssh-g-config-resolution.ts` + `ssh-config-parser.ts` | `ssh -G` / ssh_config 解析 |
| `main/ssh/ssh-host-key-verifier.ts` + `ssh-known-hosts.ts` + `ssh-host-key-store.ts` | host key 校验 |
| `main/ssh/ssh-reconnect-ladder.ts` + `ssh-reconnect-error-classification.ts` | 重连退避与错误分类 |
| `main/ssh/ssh-relay-deploy.ts` + `ssh-relay-session.ts` | 远端 relay 部署/启动/会话 |
| `main/ssh/ssh-channel-multiplexer.ts` + `relay-protocol.ts` | JSON-RPC 帧与多路复用 |
| `main/ssh/ssh-port-forward.ts` + `ssh2-port-forward-provider.ts` | 端口转发 |
| `main/ssh/system-ssh-command.ts` + `system-ssh-args.ts` | 系统 OpenSSH 兜底 |
| `relay/relay-daemon.ts` + `relay/pty-handler.ts` | 远端守护进程 |
| `renderer/components/terminal-pane/TerminalSshReconnectOverlay.tsx` | 重连覆盖层 |
| `shared/ssh-types.ts` | SshTarget / SshConnectionStatus |

### 我们能否照搬

- 对 herdr Web 客户端而言，SSH 发生在 **herdr 服务端**（daemon）而不是浏览器。可照搬的是服务端设计：`ssh2` + `keepaliveInterval 15 s` + `readyTimeout 30 s` 的连接参数、`ssh -G` 解析用户 ssh_config、agent 优先 → 未加密 key → passphrase 的认证顺序、只读 known_hosts + 私有信任 store 的 host key 策略。
- **重连阶梯**（表 + 稳定 60 s 重置 + flap 不计失败 + 上限 9 次）和 **8 态连接状态机 + 覆盖层 UI** 可原样搬到 herdr 的前端。
- "远端跑守护进程持有 pty、客户端断连不杀进程、重连回放最近 N KB"正是 herdr 已有 daemon 的模型，可对照校验 lease/grace 语义。

### 需要改造的点

- Orca 的远端 relay 是**版本锁定**的 Node bundle（客户端与 relay 同一 build，`ssh-reconnect-source-recovery.md` 说明），若 herdr 服务端要连远端主机，可以更简单：直接 `ssh2` 开 shell channel 作为 pty，不部署守护进程（代价是断线丢会话；或者远端装 herdr daemon 自身）。
- 浏览器端的 SSH target 管理只能是表单 + 服务端存储，密码/passphrase 提示需要从服务端推到 Web UI（Orca 用 IPC 回调，我们用 WebSocket 事件）。
- 端口转发在 Web 场景要转成"服务端本地端口 → 浏览器通过反代访问"，与桌面直接监听本机端口不同。

---

## H. 技术栈与工程

### Orca 怎么做（`package.json`、`components.json`、`electron.vite.config.ts`、`vite.web.config.ts`、`AGENTS.md`）

| 层 | 选型 | 备注 |
|---|---|---|
| 桌面壳 | Electron ^43.4.1 + electron-vite ^5 + electron-builder ^26 | 主进程只打包 `@xterm/headless`、`@xterm/addon-serialize`、`psl`、`zod`，其余 deps external（`electron.vite.config.ts:10-17`） |
| 前端框架 | React ^19.2.8 + TypeScript ^7.0.2 | `typescript-api: npm:typescript@6.0.3` 另装一份给工具用 |
| 状态管理 | **zustand ^5**，单 store + 40 余个 slice（`store/index.ts:1-47`，`store/slices/` 共 174 个文件） | 有 `bench:zustand-selector-fanout` 基准和 `store-listener-census` 监控 selector 扇出 |
| UI 组件 | **shadcn（new-york-v4 风格，neutral 基色，CSS variables）** + `radix-ui ^1.6`，`components.json` 指向 `src/renderer/src/assets/main.css` | `components/ui/` 约 30 个原语：button、dialog、popover、command(cmdk)、select、tabs、tooltip、sheet、scroll-area、slider、switch、sonner 等 |
| 样式 | **Tailwind v4.2**（`@tailwindcss/vite`，`@import 'tailwindcss'`，`@theme inline` 绑定 CSS 变量，`@custom-variant dark (&:is(.dark *))`）+ `tw-animate-css` + `class-variance-authority` + `tailwind-merge` + `clsx` | 设计规范 `docs/STYLEGUIDE.md`："monochrome and quiet"，颜色只表状态 |
| 图标 | lucide-react ^0.577 | |
| 构建 | Vite（`vite: npm:rolldown-vite@7.3.1`，即 Rolldown 版 Vite）+ `@vitejs/plugin-react` | web 客户端单独入口 `vite.web.config.ts`（`root: src/renderer`, `base: './'`，入口 `web-index.html`，输出 `out/web`） |
| 编辑器/预览 | monaco-editor ^0.55 + `@monaco-editor/react`、TipTap 3、react-markdown + rehype/remark 系、mermaid ^11、katex | |
| 拖拽/虚拟列表 | `@dnd-kit/core` + `@dnd-kit/sortable`、`@tanstack/react-virtual` | pane 内部拖拽是自研 pointer 实现，不用 dnd-kit |
| i18n | i18next ^26 + react-i18next，locales `en/es/ja/ko/zh`，键为 `auto.<path>.<hash>` + 英文默认值内联 | 有 `verify:localization-*` 脚本保证提取覆盖 |
| 通知 | sonner ^2 | |
| 主进程库 | node-pty ^1.1（打补丁）、ssh2、ws ^8、@parcel/watcher、zod ~4.5、yaml、tweetnacl、qrcode、posthog-node、electron-updater | |
| Lint/Format | **oxlint ^1.80 + oxfmt ^0.65**（不是 eslint/prettier），`oxlint --type-aware`、react-doctor 插件、husky + lint-staged | 硬规则：`max-lines`（约 300 行）不可 disable、禁 `helpers/utils` 命名、注释只写 WHY（`AGENTS.md`） |
| 测试 | vitest ^4（`config/vitest.config.ts`，环境 node + happy-dom，`testTimeout 30 s`）；@testing-library/react；Playwright（`tests/e2e`，electron-headless project，大量终端渲染/性能 golden 用例）；`terminal-perf` 基准脚本 | 单测文件与源文件同目录 `*.test.ts` |
| 包管理 | pnpm 12，`minimumReleaseAge: 4320` 分钟（依赖发布 3 天后才允许安装，供应链防护），`shamefullyHoist: true`，`allowBuilds` 白名单控制原生模块编译 | |
| 工程守卫 | 一批 ratchet 脚本：`check:max-lines-ratchet`、`check:ts-nocheck-ratchet`、`check:runtime-electron-ratchet`、`verify:renderer-boot-graph`、`check:reliability-gates` | 防止代码质量回退 |

代码组织：`src/main`（主进程）、`src/preload`、`src/renderer/src`（React）、`src/shared`（两侧共用纯 TS，如 keybindings / terminal-* 策略）、`src/relay`（远端守护进程）、`src/cli`、`src/types`。Web 版通过 `src/renderer/src/web/preload-api/web-*-api.ts` **把 Electron preload API 逐个换成浏览器实现**（localStorage + WebSocket RPC），业务组件不感知运行环境。

### 我们能否照搬

- **React 19 + Vite + Tailwind v4 + shadcn(new-york) + zustand + lucide + sonner + cmdk** 与我们的 React/Vite 项目完全同构，可直接对齐版本与 `components.json` 配置；`main.css` 的 token 命名（`--background/--foreground/--sidebar-*/--editor-surface`）和 `@theme inline` 写法可照抄。
- `src/shared` 里的纯 TS 模块（keybindings、terminal-scrollback-policy、terminal-contrast-correction、terminal-themes、terminal-custom-themes、bracketed-paste 消毒等）**零依赖可复制**。
- "preload API 抽象 + web 实现替换"的分层值得借鉴：把 pty/clipboard/settings/keybindings 定义成接口，Electron 与浏览器各给一套实现。
- oxlint/oxfmt + vitest + Playwright 的组合可以直接采用；`minimumReleaseAge` 值得在我们的 pnpm workspace 里也配。

### 需要改造的点

- Orca 的 zustand store 极大（174 个 slice 文件），我们初期只需 terminals / layout / settings / connection 四五个 slice。
- 不需要 electron-vite；只保留 `vite.web.config.ts` 这一套。
- Rolldown 版 Vite（`rolldown-vite`）仍处于预览，我们用标准 Vite 即可。
- i18n 的 `auto.<hash>` 键方案依赖 Orca 自研提取脚本，不建议照搬。

---

## F. cloud/ 目录（Orca Relay）与 `orca serve` 浏览器客户端

### Orca 怎么做

**1. cloud/ 是什么**：手机 ↔ 桌面的 **WebSocket 中转（relay）服务**，独立 pnpm workspace（`cloud/README.md`）。手机和桌面都不直接互连，各自向 relay cell 发出站 WebSocket，relay 配对后 splice 两条连接的帧；director 负责把 host 分配到 cell 并处理迁移。服务端技术栈（`cloud/apps/relay/package.json`）：Node + **hono**（`@hono/node-server`）+ `ws` + `pg`（Cloud SQL PostgreSQL，测试用 SQLite）+ `jose`（JWT）+ `tweetnacl` + zod；部署在 GCE，Terraform 管理（`cloud/infra/terraform`），24 个 GitHub workflow 全部默认关闭（`ORCA_CLOUD_OPERATIONS_ENABLED` 未设）。

**2. 协议（`cloud/packages/relay-contract/src/`）**

- 全部用 **zod schema** 定义 JSON 控制消息，字节串用 base64/base64url 正则约束（`wire-scalars.ts:3-11`：32 字节 base64url 为 43 字符、`RelayHostId` 16 字符）。
- Host 侧握手 `control-messages.ts:21-70`：`HostHello{v:1, relayHostId, assignmentEpoch, hostPublicKeyB64, appVersion, previousGeneration?, controlResumeSecret?}` → relay 发 `HostChallenge{challengeId, relayEphemeralPublicKeyB64, nonceB64, ciphertextB64, expiresAt}`（用 host 公钥加密的挑战，证明 host 持有私钥）→ `HostChallengeAck{proofB64}` → `HostHelloAck{generation, controlResumeSecret, leaseExpiresAt, activeConnIds, pendingConns}`；随后 relay 用 `ConnectionOpen{connId, connTicket, kind:'invite'|'resume', relayDeviceId, attachDeadlineMs}` 通知 host 有客户端要接。
- 客户端侧 `credential-messages.ts:4-37`：`RelayAuth{v:1, mode:'connect', credential(32B)}` → `RelayHello{ok, credentialKind:'invite'|'resume', leaseExpiresAt, acceptedAs:'current'|'grace', resumeExpiresAt…}`；invite 是一次性配对码，resume 是长期 token（可轮换，有 grace 版本）。
- Director（`director-messages.ts`）：`AssignmentRequest{relayHostId, reconnect?, preferredRegion?}` → `AssignmentResponse{cellUrl(https origin), assignmentEpoch, lease(签名)}`；客户端用 `ResolveRequest{relayHostId, resumeToken}` 找 host 所在 cell；迁移时 relay 发 `RelayMoved{cellUrl, assignmentEpoch}`，客户端只信任来自配置的 director origin 且 epoch 更大的搬迁（`:58-68`）。
- Close codes `close-codes.ts:1-8`：`4401 BAD_OUTER_CREDENTIAL / 4404 HOST_OFFLINE / 4408 PEER_DROPPED / 4409 WRONG_CELL / 4429 LIMIT_EXCEEDED / 4503 DRAINING`。
- Splice 状态机 `splice-state-machine.ts:1-34`：`pre-auth-admitted → credential-lease-reserved → host-notified → attach-pending → host-attached → client-acknowledged → spliced → e2ee-confirmable → teardown`，任一状态可直接 teardown；只有双向转发 handler 都装好才对客户端 ack（防"假 splice"）。
- 限额 `protocol-limits.ts`：首帧 2 s、HTTP body 4 KB、**帧最大 8 MiB**（因为桌面 worktree 目录响应已超 1 MiB）、每 host 最多 8 连接、空闲 10 min、invite 有效 10 min / 5 次尝试、resume token 30 天、relay token 5 min、控制 ping 15 s / 静默 75 s 断。`admission-budgets.ts`：pre-auth 连接上限 45、每源 4、每源每分钟 30 次；splice 低/高水位 64 KB / 256 KB，硬上限 8 MiB+256 KB，wedged 10 s；每 cell 硬上限 600/1000/3000 连接。
- **E2EE**：relay 只 splice 密文。桌面 ↔ 客户端用 tweetnacl `box`（Curve25519 + XSalsa20-Poly1305），v2 再用 HKDF 派生双向 key（见主题 E）。桌面侧接入代码 `src/main/runtime/relay/desktop-relay-service.ts`、`relay-http-client.ts`、`rpc/relay-transport.ts`（`CloudRelayTransport`，消息上限 1 MiB，`:6`）。

**3. `orca serve` 与浏览器客户端（与我们最相关）**

- `orca serve --port --pairing-address --no-pairing --mobile-pairing`（`src/main/startup/serve-mode-argv.ts:11-22`）在 headless 模式启动同一个 Electron 主进程（无窗口），起 **HTTP(S) + WebSocket 服务**（`src/main/runtime/rpc/ws-transport.ts:172-205`）：`http.createServer(staticHandler)` 托管 `out/web` 静态文件（`main-process-serve.ts:18` 找 `web-index.html`），同一端口挂 `WebSocketServer({server, maxPayload: 1 MiB})`，`MAX_WS_CONNECTIONS = 128`，服务端心跳 15 s（`:9-25`）；端口默认 6768，占用时回退到持久化的备用端口再回退 OS 分配。支持 TLS（`tlsCert/tlsKey`）。
- **配对**：服务端打印/展示 `orca://pair?...` 或 `https://host:port/?pairing=<base64 payload>` 链接（QR），payload 解码为 `WebPairingOffer{v:2, endpoint(ws url), deviceToken, publicKeyB64, pairedDeviceId?, scope}`（`web-pairing.ts:5-12`）；浏览器 `main.tsx` 从 `?pairing|pair|code|token` 或 `#hash` 读取（`web-pairing.ts:36-60`），保存到 localStorage 后清掉地址栏。每个客户端一个可撤销 token（docs "Shared Server Access"）。
- **握手**（`web-runtime-connection-transport.ts:163-203`, `frame-router.ts:49-68`）：WebSocket 打开 → 客户端生成临时 keypair，发明文 `{type:'e2ee_hello', publicKeyB64}` → 用服务端公钥 `nacl.box.before` 派生 sharedKey → 收到 `{type:'e2ee_ready'}` 后发加密的 `{type:'e2ee_auth', deviceToken, clientCapabilities[]}` → connected。之后所有 JSON RPC 为 `base64(nonce‖ciphertext)` 文本帧，二进制流为原始 `nonce‖ciphertext` 二进制帧（`socket.binaryType='arraybuffer'`）。连接超时 12 s、握手 10 s、重连退避 `[500, 1000, 2000, 4000, 8000, 15000]` ms 加抖动（`:23-25`）；客户端心跳 10 s 一次、25 s 无入帧发探针、探针 20 s 无回应判死（`web-runtime-connection-heartbeat.ts:3-5`），并且用 `installWindowVisibilityInterval` 在页面不可见时暂停、恢复可见时重新基线。
- **终端数据流**：`terminal.subscribe` 流式 RPC（`terminal-subscribe-method.ts:11-56`），客户端声明 `capabilities.terminalBinaryStream = 1` 后走 **二进制帧**（`shared/terminal-stream-protocol.ts`）：

```
16 字节头：kind 0x74 | version 1 | opcode | 0 | streamId u32 LE | seq u64 LE(高32+低32)，后接 payload
opcode: Output=1 SnapshotStart=2 SnapshotChunk=3 SnapshotEnd=4 Resized=5 Error=6
        Input=7 Resize=8 Subscribe=9 Unsubscribe=10 SnapshotRequest=11 Metadata=12
        Ack=13 ClaimViewport=14 OutputSpan=15 SetOutputPaused=16 WriteUnavailable=17
```

  服务端 `terminal-output-batcher.ts:9` 以 **5 ms** 窗口合批、单批最大 64 KB、单帧 chunk 48 KB（`terminal-multiplex-flow-control.ts:1-2`）；**基于 Ack 的滑动窗口流控**：每流初始窗口 512 KB、最大 2 MB，每连接总窗口 2 MB–8 MB，客户端每累计 192 KB 或 4 ms 发一次 Ack（`:3-9`）；每连接最多 128 个活动流。订阅开始先发 `SnapshotStart/Chunk/End`（来自主进程 headless 快照，见主题 A）再接 live `Output`。每个订阅在客户端用**独立子 WebSocket 连接**（`web-runtime-client.ts:59-60`，`files.watch` 除外共享），避免大流阻塞 RPC。
- 浏览器端替换层：`src/renderer/src/web/preload-api/web-*-api.ts` 把 Electron `window.api` 换成浏览器实现——`web-terminal-api.ts` 里本地 pty 方法全部 no-op/拒绝（终端完全走 runtime RPC），`web-clipboard-api.ts` 走 `navigator.clipboard`，**HTTP 非安全上下文用 `document.execCommand('copy')` + 拦截 `copy` 事件兜底**（`web-clipboard-copy-fallback.ts:1-29`，注意要 `stopImmediatePropagation` 防 xterm 自己的 copy 监听覆盖），`web-keybindings-api.ts` 把 keybindings 存 localStorage（`orca.web.keybindings.v1`，`web-storage.ts:1-11`），settings/ui/session 同样各一个 localStorage key。
- Web 客户端不支持的：本地 pty、SSH target 管理、文件下载（依赖 Electron 保存对话框）、Electron menu 快捷键。
- `runtime-serve-terminal-smoke.mjs` 冒烟：起 `out/main/index.js --serve` → 用 pairing code 配对 → `orca terminal create` → 发命令 → 断言输出回流 → 关停；注释说明"只有 PTY 往返才能证明服务真的能用"。

**4. 核心文件清单**

| 文件 | 职责 |
|---|---|
| `cloud/packages/relay-contract/src/control-messages.ts` | host 握手 schema |
| `cloud/packages/relay-contract/src/credential-messages.ts` | 客户端 invite/resume 凭据 |
| `cloud/packages/relay-contract/src/director-messages.ts` | cell 分配与迁移 |
| `cloud/packages/relay-contract/src/splice-state-machine.ts` | splice 状态机 |
| `cloud/packages/relay-contract/src/protocol-limits.ts` + `admission-budgets.ts` | 限额 |
| `cloud/apps/relay/src/relay-server.ts` + `app.ts` | hono + ws 服务端 |
| `src/main/runtime/rpc/ws-transport.ts` | 桌面/serve 侧 HTTP+WS 服务器 |
| `src/main/runtime/rpc/methods/terminal/terminal-subscribe-method.ts` + `terminal-legacy-subscribe-binary.ts` | 终端流订阅 |
| `src/shared/terminal-stream-protocol.ts` + `terminal-multiplex-flow-control.ts` | 二进制帧与流控常量 |
| `src/renderer/src/web/main.tsx` + `WebConnect.tsx` | 浏览器入口与配对页 |
| `src/renderer/src/web/web-runtime-connection-transport.ts` + `-frame-router.ts` + `-heartbeat.ts` | WS 传输、E2EE 握手、心跳 |
| `src/renderer/src/web/web-e2ee.ts` | tweetnacl 封装 |
| `src/renderer/src/web/web-pairing.ts` | 配对 URL 解析 |
| `src/renderer/src/web/preload-api/*` | 浏览器版 preload API |
| `vite.web.config.ts` + `config/scripts/project-renderer-web-client.mjs` | web 构建 |

### 我们能否照搬

- **`orca serve` 的形态就是 herdr Web 客户端要做的事**：同端口托管静态前端 + WebSocket RPC，配对链接携带 token 与服务端公钥，浏览器 localStorage 记住环境。可以直接借鉴其 URL 方案（`?pairing=` 一次性读取后清地址栏）与握手三步。
- **终端二进制帧格式（16 字节头 + opcode + streamId + seq）与 Ack 滑动窗口流控**是成熟设计，建议照搬（含常量：5 ms 合批、48 KB chunk、512 KB 初始窗口、192 KB/4 ms ack）。
- 客户端心跳"可见时 10 s、探针 20 s、隐藏时暂停"的策略适合手机浏览器。
- HTTP 明文下的 `execCommand('copy')` 兜底、`stopImmediatePropagation` 防 xterm 抢 copy 事件，这两个坑值得直接吸收。
- 每个订阅一条独立 WebSocket 的做法可以借鉴（或用单连接多流 + 流控，Orca 两者都做了）。

### 需要改造的点

- cloud relay（director/cell/Cloud SQL/Terraform）是为公网穿透手机场景服务的重基础设施，herdr 若走 Tailscale/反代/自有 HTTPS 可以**整体不要**；只在需要 NAT 穿透时参考其 invite/resume 双凭据 + E2EE 模型。
- Orca 浏览器端 E2EE 是因为 relay 不可信；若 herdr 走 WSS + 服务端 token 鉴权，可去掉应用层加密，直接用 TLS。
- Orca 的 RPC 方法面极大（几十个 `web-*-api.ts`），herdr 只需 terminal / layout / settings / connection 少数方法。
- 服务端主进程仍是 Electron headless（`out/main/index.js --serve`），Orca 自己也在做 Node-only 的 `orcad`（`smoke:orcad-terminal`）；herdr daemon 天然是 Node/Go 进程，无此包袱。

---

## 总结：对 herdr Web 客户端最有价值的十条

1. **xterm 6.x + fit/unicode11/web-links/search/serialize + 懒加载 webgl**，GPU 三态设置，context loss 后降级 DOM 且 60 s 内 3 次不再重试（A）。
2. **输出调度器**：前台高优先、`write` 回调背压、积压上限 `max(2 MB, scrollback×120)`，服务端 5 ms 合批 + Ack 滑动窗口（A、F）。
3. **scrollback 持久化靠服务端 headless xterm 快照**（snapshotAnsi + scrollbackAnsi + modes），前端按 `[normalPrologue, scrollback, altPrologue, altFrame]` 顺序回放（A）。
4. 主题就是 `Record<name, ITheme>`，切换时值比较后写 `options.theme`，`minimumContrastRatio` 按背景亮度 4.5/3；没有 Cobalt2，需自建（B）。
5. 分屏是严格二叉树 `{type:'split', direction, first, second, ratio}`，**拖动时只 fit 本地、drop 才发 pty resize**，ResizeObserver 后等稳定帧（最多 8 帧）再 fit（C）。
6. 快捷键引擎 `src/shared/keybindings` 纯 TS 可直接复制；`orca-first | terminal-first` 二选一 + `allowInTerminal` 白名单解决 ctrl+b 类冲突；Ctrl+C 有选区才复制否则发 SIGINT（G）。
7. 手机端虚拟键条键表与字节生成器（ctrl/alt/CSI/SS3 规则）可直接搬；48 ms 写合并、按光标行键盘避让、TUI 鼠标模式下滑动转滚轮报告、visibilitychange 后 clearTextureAtlas（E）。
8. 前台恢复三条教训：回前台立即探活并重置重连计数、半开连接需要主动探针、换 client 后 hook 要重读（E）。
9. SSH 在服务端：ssh2 + `ssh -G` 解析配置 + 只读 known_hosts + 私有信任 store；重连阶梯 `[1,2,5,5,10,10,10,30,30]s`、稳定 60 s 重置、flap 不计失败（D）。
10. `orca serve` = 同端口静态前端 + WS RPC + 配对 URL（token + 服务端公钥）+ localStorage 记住环境，正是我们要做的产品形态；HTTP 明文下剪贴板用 `execCommand('copy')` 兜底（F）。
