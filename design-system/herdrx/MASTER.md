# herdrx Design System

> 2026-09-06 用户明确要求分成两类：工作台之前是普通网站 UI，以白灰浅色为主；工作台才采用 Terminal 形式。网站和工作台分别保存外观，终端 ANSI 主题独立。

## Product character

- Quiet, precise developer tool; dense enough for daily work without looking cramped.
- 网站默认中性白灰表面与克制的蓝色按钮，采用常规表单和列表排版。工作台默认蓝灰表面与低饱和金色强调。避免装饰渐变和发光。
- No glassmorphism, decorative gradients, emoji icons, or motion without meaning.
- Desktop mirrors Herdr's workspace/tab/pane hierarchy. Mobile prioritizes attention and one terminal at a time.

## Brand identity

- Logo 与应用图标使用生成的银白/淡金交错 H 和向前箭头；原稿、导出与生成提示词见 [品牌素材](brand/README.md)。
- 网站导航共用 96×24 px 横向标识，字标随当前界面文字色适配，工作台与加载页使用同一图标。
- 网站 favicon、手机桌面与 PWA 均使用 PNG/ICO 导出；不复用旧的字母 H 占位图标。

## Color tokens

| 用途 | Cobalt 深色 | 浅色 |
|---|---|---|
| 页面 | `#142c3c` | `#f7f8fa` |
| 面板 | `#193747` | `#fefefe` |
| 抬升表面 | `#234354` | `#f0f2f5` |
| 输入 | `#122938` | `#fdfdfe` |
| 分隔线 | `#345161` | `#dde1e6` |
| 控件边框 | `#68828f` | `#858e9a` |
| 正文 | `#e2ecf0` | `#1f2937` |
| 次级文字 | `#abc0ca` | `#5b6472` |
| 强调 | `#dbc37e` | `#3766a3` |
| 强调表面上的文字 | `#203343` | `#f7f9fc` |

完整语义变量以 `web/src/ui-theme.css` 为准。文字与状态色对比度至少 4.5:1，控件边框对相邻表面至少 3:1。禁用状态另行淡化。终端使用 `web/src/lib/themes.ts` 的独立色板。

Never communicate agent state by color alone. Pair color with text and a dot/symbol.

## Typography

- UI: `Noto Sans CJK SC`、`Noto Sans SC`、`PingFang SC`、`Microsoft YaHei` 与无衬线回退。正文桌面 14px、手机 16px，表单在手机至少 16px。
- Terminal: `JetBrains Mono`, `SFMono-Regular`, `Consolas`, system monospace. Default 14px.
- Numeric counters and connection timings use tabular figures.
- Do not download Google Fonts at runtime; use system fallbacks and optional self-hosted assets.

## Spacing and shape

- 4px base grid: 4, 8, 12, 16, 24, 32.
- Control height: 36px desktop, 32px in compact website navigation, at least 44px on touch screens.
- Radius: 6px controls, 8px lists/cards, 10px dialogs.
- Shadows are reserved for floating menus/dialogs; panels use borders.
- Terminal pane dividers use a large invisible hit target with a 1px visible line.

## Motion

- 120–200ms opacity/color/transform transitions only.
- No entrance choreography in the workbench.
- Respect `prefers-reduced-motion`; terminal output never animates.
- Dragging follows the pointer immediately; commit remote resize only on release.

## Responsive behavior

- `<768px` 及紧凑触屏横屏：单终端、主机栏 + 终端标题栏两层顶部、切换面板和底部辅助键；字号/缩放移入设置。
- `768–1023px`: collapsible sidebar; pane split handles have at least 24px touch hit area.
- `>=1024px`: persistent sidebar + tab bar + BSP terminal surface.
- 桌面当前终端标题栏整合字号、缩放及适应窗口：普通桌面 32 px 单行、触屏 44 px；按分屏实际宽度收起快捷缩放，全部选项保留在工作台设置。
- Use `100dvh` and safe-area insets; never disable browser zoom.
- Critical actions always have visible controls; gestures are optional accelerators.

## Accessibility and interaction

- Text contrast 4.5:1; large glyphs/non-text UI 3:1.
- Visible 2px focus ring using `--accent`; never remove focus without replacement.
- All icon buttons have an accessible label and tooltip on desktop.
- Tab order follows the visual hierarchy; Escape closes the top overlay.
- Async actions show pending state and preserve a clear retry path.
- Destructive actions require confirmation and are spatially separated.
- Touch targets are at least 44×44px with 8px separation.
- Toasts use `aria-live="polite"` and never steal focus.

## 自绘控件与提示

- 应用内的下拉菜单、确认弹窗、日期时间选择、悬停提示和表单校验均使用项目组件，不调用浏览器原生选择器或 `window.alert/confirm/prompt`，不使用 DOM `title` 弹出提示。
- 统一入口为 `Select`、`Modal` / `useConfirm`、`DateTimeField`、`Tooltips` 和 `Form`。按钮提示使用 `data-tooltip`；表单使用 `Input` / `Textarea` / `Field`，错误与输入关联，修正错误时保留输入。
- 交互使用无样式的 Radix primitives，外观由 `controls.css` 和现有主题变量绘制。弹层支持方向键、Enter、Escape、焦点返回、滚动列表和视口避让；嵌套 Escape 只关闭最上层。
- 确认弹窗默认聚焦“取消”，明确说明目标与后果；页面卸载或切换主机取消待确认操作。打开外部链接在确认按钮的点击中执行。
- 日期时间按浏览器本地时区选择，确认后才更新筛选条件；小时与分钟使用文本控件，校验范围后应用。
- 文件选取、浏览器通知授权、密码管理器等浏览器或操作系统界面由相应平台提供，网页不冒充或绕过这些界面。
- `test-custom-controls.mjs` 检查源码中的原生 UI 回退，并在 Chromium、Firefox、WebKit 中验证交互与布局。

## Terminal-specific rules

- Cobalt2 is the default; enhanced contrast is a separate toggle because xterm contrast correction changes original colors.
- WebGL is optional and must fall back to DOM after repeated context loss.
- OSC 52 clipboard writes are disabled by default. OSC 8 links allow only `http` and `https` and never open automatically.
- The xterm host DOM must remain stable across React layout updates.
- Mobile input supports IME composition, visible modifier state, and explicit observe/control status.

## 管理页面

- 登录表单左对齐，保留注册关闭/邀请入口和可信实例说明。
- 桌面主机列表每行突出名称、连接说明和打开操作；手机使用紧凑卡片。搜索名称、地址或接入方式。
- 访问管理把注册开关、用户筛选、身份/状态/操作明确分组；账号 ID 和创建时间收进详情。
- 网站顶部导航普通桌面 44px、触屏 52px，用户名和角色单行；窄屏导航用带可访问名称的图标按钮保留所有入口。
- 界面主题只分浅色、深色，用按钮直接切换，当前图标和文案随之更新；支持 Enter/Space，刷新保留选择，不随操作系统变化。
- 网站与工作台分别保存主题，同类页面跨标签同步；在工作台设置中只调整工作台外观，不能影响网站页面或重连终端。

## PWA 安装后的使用体验

- Mac 和手机的浏览器安装是固定产品要求；安装后的独立窗口沿用同一套 UI、名称与图标。
- 布局同时适配浏览器标签页与 standalone 窗口，在没有浏览器导航栏时保留应用内必要的返回、主机切换与设置入口。
- 验证程序坞/主屏幕启动、窗口调整、软键盘、安全区域和再次打开工作台；真实设备安装状态以发版清单为准。
- PWA 安装确认由浏览器/操作系统提供，应用内引导仍使用项目组件。
- `manifest.webmanifest` 的 `theme_color` 保持网站默认浅色 `#f7f8fa`；工作台深色由 `boot.js` 与 `appearance.ts` 在运行时改写 `theme-color` meta，不把 manifest 做成动态文件。

## Pre-delivery viewport checks

- 375×667 phone portrait
- 844×390 phone landscape
- 768×1024 tablet
- 1024×768 compact desktop
- 1440×900 desktop

For every viewport verify focus order, safe areas, no accidental horizontal page scroll, terminal fit, control ownership labeling, and reduced motion.
