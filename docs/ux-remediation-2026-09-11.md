# herdrx 体验修复（2026-09-11）

审查总表见 `.local-notes/ux-review-2026-09-11/README.md`（38 项）。本文件记录合并进 `main` 后的处理结果。编号沿用审查总表。

验证以 Chromium Playwright、vitest、Go 测试与 Docker 镜像冒烟为主。Firefox/WebKit、真机 iOS/Android/Mac PWA、真实 Herdr 流量未作为本轮验收。

## 状态总览

| 编号 | 主题 | 状态 | 承担 |
|---|---|---|---|
| 1 | 切 pane 后焦点跟随 | 完成 | wb-input |
| 2 | Ctrl+B 前缀 keymap | 完成 | wb-input |
| 3 | `#pair=` 深链回列表 | 完成 | pages |
| 4 | Tailcat 失败文案不可见 | 完成 | pages |
| 5 | 配对等待不能关闭 | 完成 | pages |
| 6 | SSH 指纹撑破手机网格 | 完成 | pages |
| 7 | SSH 端口删空变 0 | 完成 | pages |
| 8 | 推荐不存在的 pair 命令 | 完成 | pages |
| 9 | 浅色「输入方式」对比度 | 完成 | wb-mobile |
| 10 | 静态资源不压缩 | 完成 | perf-pwa |
| 11 | SW 预缓存过大 | 完成 | perf-pwa |
| 12 | boot.js 3s 误报 | 完成 | perf-pwa |
| 13 | WorkbenchPage 串行下载 | 完成 | perf-pwa |
| 14 | boot.js 同步外链 | 完成 | perf-pwa |
| 15 | PWA meta / standalone | 部分 | perf-pwa + wb-mobile |
| 16 | 固定字号裁切无提示 | 完成 | wb-status |
| 17 | 焦点边框与 pane 标签 | 完成 | wb-status |
| 18 | 断线遮罩与重连 | 完成 | wb-status |
| 19 | 分屏比例不能调 | 完成 | integrate |
| 20 | 搜索只搜可见 40 行 | 完成 | wb-input |
| 21 | 无快捷键帮助 | 完成 | wb-input |
| 22 | 右键透传后找不到菜单 | 完成 | wb-input |
| 23 | 标签栏溢出与右键先切换 | 完成 | wb-status |
| 24 | Mac Option / Shift+Enter / 链接 | 完成 | wb-input |
| 25 | 右键菜单与对比度默认 | 完成 | wb-input |
| 26 | 24px 按钮与 a11y | 完成 | wb-input |
| 27 | 软键盘滚走顶栏 | 部分 | wb-mobile |
| 28 | 手机顶栏无状态 | 完成 | wb-status |
| 29 | 无法选择/复制终端文本 | 完成 | wb-mobile |
| 30 | 横屏+键盘高度归零 | 完成 | wb-mobile |
| 31 | toast 压住输入框 | 完成 | wb-mobile |
| 32 | iOS 字体回退 Courier | 部分 | wb-mobile |
| 33 | 多 pane 切换两步 | 完成 | wb-status |
| 34 | overscroll / touch-action | 完成 | wb-mobile |
| 35 | 站点顶栏只剩图标 | 完成 | wb-mobile |
| 36 | 英文枚举满屏 | 完成 | wb-status |
| 37 | 主机列表骨架/空态/搜索 | 部分 | pages |
| 38 | 术语、CSS、表单杂项 | 部分 | pages + integrate |

## 逐条

1. **完成。** 切 pane、侧栏 Agent、切换器后 `focusDirectInput()`；`TerminalPane` 在成为 active 且焦点不在 composer/对话框时 `terminal.focus()`。验证：vitest + `wb-input-repro.mjs`。
2. **完成。** `keymap.ts` 表驱动；忽略纯修饰键；Ctrl+B Ctrl+B 发送 `0x02`；composer 内不进 prefix。验证：vitest keymap / WorkbenchPage。
3. **完成。** `#pair=` 后 `replaceState('/pair')` 并 `dispatch PopStateEvent`；`App.routePath()` 在 hash 仍为 `#pair=` 时视为 `/pair`。
4. **完成。** Tailcat 错误移到凭据下方并 `scrollIntoView`，失败保留凭据。
5. **完成。** `Modal.allowCloseWhileBusy`；等待可关、显示已等待秒数，任务后台继续。
6. **完成。** `.field-hint { overflow-wrap: anywhere }`，表单 `min-width: 0`。
7. **完成。** 端口 draft 存字符串，提交时 `Number()` 校验 1–65535。
8. **完成。** 无效配对页改为 `~/.local/bin/herdrx pair` 或走添加主机。
9. **完成。** 输入方式按钮使用 `--ui-input` / `--ui-text`；夹具对比度 ≥4.5。
10. **完成。** 构建期生成 `.br`/`.gz`，Go 按 `Accept-Encoding` 直出。工作台 `/h/` 冷加载约 224KB。
11. **完成。** 预缓存只含 shell、assets、实际引用图标；未用品牌图移出 `public`。
12. **完成。** boot 回退改到 `load` 后 8s 或 script `error`；下载中只提示仍在加载。
13. **完成。** `/h/` 路径并行预热 `WorkbenchPage` chunk。
14. **完成。** `boot.js` 内联，CSP 用 sha256，无 `unsafe-inline`。
15. **部分。** 已补 apple-mobile-web-app-title、status-bar-style、192 maskable、orientation、`interactive-widget`；standalone 下隐藏安装条，iOS 无 Notification 时提示先加到主屏幕。真机 Mac/iPhone/Android 安装未执行。
16. **完成。** 固定字号且远端行列大于视口时，左下角可点徽标切到适应窗口。`docs/display-validation-2026-09-06.md` 已改为电脑默认固定 14px。
17. **完成。** 焦点 pane 边框 `--accent`；桌面常驻 `pane-status-chip`。窄于 560px 的分屏会隐藏标题栏里的 `.terminal-title` 并让工具换行，chip 仍在标题栏外，合并后保留可见。
18. **完成。** `retryNow()`、中文状态、顶部条幅、offline 半透明遮罩。
19. **完成。** 相邻 pane 1px 分隔处 6px 透明拖拽热区（桌面 pointer），松手调用 `layout.set_split_ratio`。`prefix+r` 进入 RESIZE，hjkl 调 `pane.resize`，Esc/Enter 退出。手机不渲染热区。Go 侧本已放行这两个方法。
20. **完成。** 打开搜索先 `pane.read` 历史；Enter/Shift+Enter、计数、Ctrl+Shift+F / Cmd+F。
21. **完成。** `prefix+?` 与设置/侧栏「快捷键」入口。
22. **完成。** 透传时显示 pane 菜单按钮；Shift+右键恒开 herdrx 菜单。
23. **完成。** 标签栏横滚与溢出渐隐；右键不先切换。
24. **完成。** Option 作为 Meta；Shift+Enter 走 `pane.send_keys`；链接需 Cmd/Ctrl+点击。真机 Mac 手感未测。
25. **完成。** 右键复制/粘贴/全选/搜索；对比度默认关；草稿 `localStorage`；`beforeunload`。
26. **完成。** 桌面 tool-button 24px；ContextMenu 还原焦点；屏幕阅读器开关；中文 aria-label。
27. **部分。** 工作台锁到 visualViewport 高度，`offsetTop` + `position:fixed`，禁止页面滚动。真机 iOS 软键盘未测。
28. **完成。** 手机顶栏 StatusDot、连接点、其他 pane blocked 角标。
29. **完成。** 「选择文本」开关与「复制屏幕」；非安全上下文弹出可全选文本框。
30. **完成。** `.workbench-short` 收矮顶栏/composer；短布局下辅助键与输入框互斥。短布局（横屏+软键盘）顶栏与触点收到 32px 是为保住终端行数的刻意权衡，待真机确认。
31. **完成。** toast/mode-bar 使用 `--dock-height`；toast 5s 消失。
32. **部分。** 等宽栈含 `ui-monospace, Menlo`；`fonts.ready` 后再 fit。iOS Courier 回退待真机。
33. **完成。** 多 pane 时手机 chip 行一键切换。
34. **完成。** overscroll-behavior、touch-action:manipulation；工具栏不再自动 focus 弹 tooltip。
35. **完成。** ≤600px 站点顶栏改为「更多」菜单，退出需确认。
36. **完成。** `labels.ts` 中文映射；计数、通知、StatusDot aria-label。
37. **部分。** 骨架屏 shimmer、失败空态+重试、搜索匹配用户名/文件夹、文件夹计数含子树。**选中父文件夹时列表显示子树主机**（与子树计数一致；审查时旧行为只显示直接子项）。主机在线/离线需要后端 `last_seen`，本轮未做。
38. **部分。** 密码显隐、Toast 成功反馈、空态、注册开关确认、SSH 保存提示、`autoCapitalize=none` 已做。用户可见「Pane」统一为「终端」，`...`/`「」` 改为 `…`/`“”`。styles.css 去掉已被 ui-theme 覆盖的颜色，`.xterm-dim` 改为基于 `--terminal-ink` 的 `color-mix`，深色下粘贴反馈按钮 hover 可见，`.notice` 圆角 6px，删除 ui-theme 中被 controls.css 覆盖的 `.button-danger`，`.button` 基类 `background: transparent`。管理员「重置密码」前后端都没有，未做。

## 验证方式

- 各 lane：`pnpm --dir web typecheck/lint/vitest`、`make web-build` 及契约内 Playwright。
- 合并后主树：CLAUDE.md 全量门禁（见 integrate 结果文件）。
- 人工抽查：用审查证据目录夹具复现 3 个已修条目（见 integrate result）。

## 待真机 / 未做

- 真实 Mac/iPhone/Android PWA 安装与通知权限。
- iOS 软键盘滚页、Courier 回退、双击缩放。
- 主机在线/离线（`last_seen`）。
- 管理员重置密码。
- Firefox/WebKit、HTTP+IP 非安全上下文剪贴板、真实跨网 Herdr。
