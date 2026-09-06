# herdr 用户交互模型调研报告（供 Web 客户端复刻）

> 仓库：`../herdr`，证据以 `文件:行` 给出（相对仓库根）。文档路径缩写 `docs/` = `docs/next/website/src/content/docs/`。

## B. 完整快捷键表

### B.1 模式模型（客户端 shell 五态 + overlay）

客户端输入状态机是 `ClientShellMode { Terminal, Prefix, Navigate, Resize, Copy }`（src/client/shell/state.rs:304-310），另有一层 `ClientShellOverlay`（modal）：Onboarding / ProductAnnouncement / ReleaseNotes / Rename / ConfirmClose / Help / Navigator / WorktreeCreate / WorktreeOpen / WorktreeRemove / ContextMenu / GlobalMenu / Settings（state.rs:598-612）。服务端 `app::state::Mode` 只有 `Navigate | Terminal` 两态（src/app/state.rs:685-688），Prefix/Resize/Copy 都是客户端本地状态。

| 模式 | 进入 | 退出 | 按键归属 | 证据 |
|---|---|---|---|---|
| Terminal | 默认 | — | 先匹配 direct 绑定，再判 prefix 键，否则整键转发给焦点 pane | src/client/shell/input.rs:523-536 |
| Prefix | Terminal/Copy 下按 `keys.prefix`（默认 `ctrl+b`） | 按任意一键后**立即**回到 Terminal（或 Copy）：匹配到绑定则执行；再按一次 prefix 把字面 ctrl+b 发给 pane；Esc 取消；未识别键**静默丢弃**（不转发）。**无超时** | 只吃一键 | input.rs:537-566 |
| Navigate | `prefix+w`（workspace_picker）；手机端点 header 的 `switch` 按钮 | Esc 或再按 prefix；Enter 选中 workspace 后退出；多数动作执行后退出，但 h/j/k/l/←/→/Tab 移焦点**保持**在 Navigate | 见 B.3 | input.rs:592-816、mobile.rs:775-790 |
| Resize | `prefix+r` | Esc / Enter / 再按 prefix+r 或 r | h/←, j/↓, k/↑, l/→ 调整焦点 pane 尺寸（每次发 `ResizePane*` 动作） | input.rs:864-893 |
| Copy | `prefix+[`（copy_mode） | q / Esc（无选区无搜索时）/ y 或 Enter（复制并退出） | 见 B.4；prefix 在 Copy 模式仍进入 Prefix，之后回到 Copy | input.rs:575-583, copy_mode.rs:101-260 |
| Overlay（modal） | 各动作/鼠标 | Esc / Enter / 点击外部 | 所有按键被 overlay 吃掉，不转发 pane | input.rs:487-495 |

模式条（mode bar）文案，渲染在 pane 区域最底一行（tab_bar_position=bottom 时替换 tab 行）：
- PREFIX：`esc cancel · <prefix> send prefix · w workspace nav · ? keybinds`（src/client/shell/render.rs:71-84）
- NAVIGATE：`esc back · ↑/↓ workspace · tab pane · ? keybinds`（render.rs:85-96）
- RESIZE：`h/l width · j/k height · esc done`，背景色用 mauve 区别于其他模式的 accent（render.rs:50-54, 97-108）
- COPY：`h/j/k/l w/b/e { } move · / ? search · n/N repeat x/y · v/space select · y/enter copy · q/esc exit`（render.rs:109-160）
- ERROR：endpoint 错误时显示 ` ERROR <msg>`（render.rs:64-69）

### B.2 默认绑定总表（`[keys]`，全部可配置）

默认值来源：`impl Default for KeysConfig`（src/config/model.rs:1021-1088），字段注释（model.rs:331-459）。解析与冲突校验在 src/config/keybinds.rs。`prefix` 默认 `ctrl+b`（model.rs:1024）。所有 action 都可绑定为数组多键（keybinds.rs:1635 测试；docs/configuration.mdx:145-151）。

**全局 / 会话**

| action 字段 | 默认键 | 作用域 | 行为 | 证据 |
|---|---|---|---|---|
| prefix | `ctrl+b` | Terminal/Copy | 进入 Prefix 模式；Prefix 下再按 = 发送字面 ctrl+b | model.rs:1024; input.rs:531,547 |
| help | `prefix+?` | 任意 | 打开 keybind help overlay（可 `/` 过滤） | model.rs:1025; keybind_help.rs |
| settings | `prefix+s` | 任意 | 打开 Settings overlay（Theme/Indicators/Sound/Toast/Integrations 五段） | model.rs:1026; state.rs:409-415 |
| detach | `prefix+q` | 任意 | 断开当前客户端，server 与 agent 继续运行 | model.rs:1041; docs/quick-start.mdx:56 |
| reload_config | `prefix+shift+r` | 任意 | 重载 config.toml | model.rs:1042 |
| open_notification_target | `prefix+o` | 任意 | 跳到当前可见 toast 通知指向的 pane | model.rs:1043 |
| goto | `prefix+g` | 任意 | 打开 Navigator（会话导航/goto picker） | model.rs:1034 |
| workspace_picker | `prefix+w` | 任意 | 进入 Navigate 模式 | model.rs:1033 |
| toggle_sidebar | `prefix+b` | 任意 | 折叠/展开 sidebar | model.rs:1082 |
| remote_image_paste | `ctrl+v`（字符串非 BindingConfig） | 仅 `herdr --remote` 本地客户端 | 把本地剪贴板图片桥接到远端 | model.rs:1047 |

**Workspace**

| action 字段 | 默认键 | 行为 | 证据 |
|---|---|---|---|
| new_workspace | `prefix+shift+n` | 弹 Rename overlay 输入名字后创建 | model.rs:1027 |
| rename_workspace | `prefix+shift+w` | 重命名选中 workspace | model.rs:1031 |
| close_workspace | `prefix+shift+d` | 关闭选中 workspace（confirm_close 时弹确认） | model.rs:1032 |
| new_worktree | `prefix+shift+g` | 从当前 Git workspace 新建 worktree 子 workspace | model.rs:1028 |
| open_worktree | **默认未绑定** | 列出已有 worktree 打开 | model.rs:1029 |
| remove_worktree | **默认未绑定** | 删除 worktree checkout（确认） | model.rs:1030 |
| previous_workspace / next_workspace | **默认未绑定** | 上/下一个 workspace | model.rs:1044-1045 |
| switch_workspace | **默认未绑定**（建议 `prefix+shift+1..9`） | 按序号切 workspace | model.rs:1056; docs/configuration.mdx:171-175 |

**Tab**

| action 字段 | 默认键 | 行为 | 证据 |
|---|---|---|---|
| new_tab | `prefix+c` | 新 tab（`ui.prompt_new_tab_name` 为 true 时先弹命名框） | model.rs:1050; context_menu.rs:308-343 |
| rename_tab | `prefix+shift+t` | 重命名当前 tab | model.rs:1051 |
| previous_tab / next_tab | `prefix+p` / `prefix+n` | 切换 tab | model.rs:1052-1053 |
| switch_tab | `prefix+1..9` | 跳到第 N 个 tab | model.rs:1056 |
| close_tab | `prefix+shift+x` | 关闭当前 tab；关闭最后一个 tab 会关闭 workspace | model.rs:1058; docs/cli-reference.mdx:160 |
| move_tab_previous / move_tab_next | **默认未绑定** | 移动 tab 顺序 | model.rs:1054-1055 |

**Pane**

| action 字段 | 默认键 | 行为 | 证据 |
|---|---|---|---|
| split_vertical | `prefix+v` | 左右分屏（新 pane 在右） | model.rs:1073 |
| split_horizontal | `prefix+minus` | 上下分屏（新 pane 在下） | model.rs:1074 |
| close_pane | `prefix+x` | 关闭焦点 pane | model.rs:1075 |
| zoom | `prefix+z`（别名 `fullscreen`） | 切换焦点 pane 全屏 zoom（tab 级状态） | model.rs:1076, 434-436 |
| focus_pane_left/down/up/right | `prefix+h/j/k/l` | 方向移焦点 | model.rs:1062-1065 |
| swap_pane_left/down/up/right | `prefix+shift+h/j/k/l` | 与相邻 pane 交换位置（保留 split 形状） | model.rs:1066-1069 |
| cycle_pane_next / cycle_pane_previous | `prefix+tab` / `prefix+shift+tab` | 顺序循环焦点 | model.rs:1070-1071 |
| last_pane | **默认未绑定** | 跳回上一个焦点 pane（跨 workspace/tab） | model.rs:1072 |
| rename_pane | `prefix+shift+p` | 重命名 pane（显示在边框标题） | model.rs:1059 |
| edit_scrollback | `prefix+e` | 用 $EDITOR 打开 pane 回滚缓冲 | model.rs:1060 |
| copy_mode | `prefix+[` | 进入键盘复制模式 | model.rs:1061 |
| resize_mode | `prefix+r` | 进入 Resize 模式 | model.rs:1077 |
| resize_pane_left/down/up/right | **默认未绑定**（文档建议 `ctrl+shift+alt+方向`） | 一键调整尺寸 | model.rs:1078-1081; docs/configuration.mdx:153-160 |

**Agent**

| action 字段 | 默认键 | 行为 | 证据 |
|---|---|---|---|
| previous_agent / next_agent | **默认未绑定** | 按 agent 面板顺序切换焦点 | model.rs:1046-1047 |
| focus_agent | **默认未绑定**（建议 `prefix+alt+1..9`） | 聚焦第 N 个 agent | model.rs:1048 |

**自定义命令 `[[keys.command]]`**：`key` + `type ∈ {popup, pane, shell, plugin_action}` + `command` + 可选 `description/width/height`（keybinds.rs:296-304; docs/configuration.mdx:180-237）。popup 为会话级 modal 终端，吃掉包括 Esc 的全部输入直到命令退出。

### B.3 Navigate 模式专用键

可配置字段（不得带 `prefix+`，不得用 esc/enter/tab/shift+tab/←/→/未修饰 1-9）：

| 字段 | 默认 | 行为 | 证据 |
|---|---|---|---|
| navigate_workspace_up / down | `up` / `down` | 在 sidebar workspace 列表中移动选择光标（桌面循环、手机端夹紧不循环） | model.rs:1035-1036; input.rs:794-816 |
| navigate_pane_left/down/up/right | `h` / `j` / `k` / `l` | 移动 pane 焦点，**保持** Navigate | model.rs:1037-1040; input.rs:717-742 |

固定语义（不可配置，keybinds.rs:729-749 保留）：Esc/prefix 退出；Enter 聚焦选中 workspace 并退出；Tab / Shift+Tab 循环 pane（保持 Navigate）；← / → 永久别名 pane 左/右；`1..9` 直接切换第 N 个 workspace 并退出（input.rs:630-648, 654-699）。其它 prefix 绑定在 Navigate 下也可直接按（无需再按 prefix），例如 `c` 新 tab、`v` 分屏（input.rs:743-786）。

### B.4 Copy 模式完整键表（src/client/shell/copy_mode.rs:101-260）

| 键 | 行为 |
|---|---|
| h/j/k/l, ←↓↑→ | 移动光标 |
| w / b / e | 下一词首 / 上一词首 / 下一词尾 |
| W / B / E | 大词版本 |
| { / } | 上/下一段落 |
| 0 / Home | 行首；`^` 首个非空字符；`$` / End 行尾 |
| g / G | 历史顶部 / 底部 |
| PageUp / PageDown, ctrl+b / ctrl+f | 整页 |
| ctrl+u / ctrl+d | 半页 |
| v / Space | 开始字符选择；V 行选择 |
| y / Enter | 复制并退出 |
| / 与 ? | 前向/后向字面搜索（大小写：查询含大写才敏感）；搜索框内 Backspace 删、ctrl+u 清空、Enter 确认、Esc 取消（copy_mode.rs:262-320） |
| n / N | 重复搜索（同向/反向） |
| q | 退出不复制 |
| Esc | 有选区或搜索时先清除，否则退出 |
| prefix（ctrl+b） | 进入 Prefix 模式而非翻页（input.rs:575-579） |

Copy 模式不暂停 pane 进程，输出仍实时刷新（docs/keyboard.mdx:67）。

### B.5 Resize 模式键表（input.rs:864-893）

h/← 向左、j/↓ 向下、k/↑ 向上、l/→ 向右各触发一次 `ResizePane*`；Esc / Enter / prefix+r 退出。步长由服务端 `pane.resize` 的 amount 决定（CLI 默认 0.1 比例，docs/socket-api.mdx:75）。

### B.6 Key 字符串语法（src/config/keybinds.rs:1197-1329）

- 修饰符：`ctrl`/`control`、`alt`、`shift`、`cmd`/`super`；`shift+tab` 归一化为 BackTab（keybinds.rs:1321-1329）。
- 特殊键：enter, tab, esc/escape, backspace, left/right/up/down, home/end, pageup/pagedown, f1..f12。
- 命名标点：minus, comma, period, slash, backslash, quote, double_quote, semicolon, colon, percent, ampersand, backtick, plus（keybinds.rs:1267-1279）；大写单字符自动加 shift（1282-1284）。
- `1..9` 仅对 indexed 动作（switch_tab / switch_workspace / focus_agent）有效（keybinds.rs:832, 931）。
- 校验拒绝：无修饰可打印字符做 direct 绑定（"unsafe direct keybinding"，1063）；prefix 右侧等于 prefix 本身（"reserved keybinding"，1041）；navigate 键含 prefix+ 或 esc（997, 1007）；同键冲突时保留先注册者并禁用后者（1020, 1054）。
- Prefix 匹配对 shift 产生的字符做回退（`shift+/` → `?`，src/input/keybindings.rs:112-121, 263-273）。

### B.7 鼠标操作总表（src/client/shell/mouse.rs，默认 `ui.mouse_capture = true`）

| 手势 | 目标区域 | 行为 | 证据 |
|---|---|---|---|
| 左键单击 | pane 内容 | 聚焦 pane；若应用开启 mouse reporting 则转发给应用，否则开始文本选区锚点 | mouse.rs:2142-2195 |
| 左键拖动 | pane 内容 | 拉选区，边缘自动滚动；松开时若 `copy_on_select` 则复制 | mouse.rs:1661-1690 |
| 双击（≤ 双击间隔） | pane 内容 | 选中并复制一个 token/词 | mouse.rs:2165-2172 |
| ctrl+左键 | pane 内容 | 激活链接（OSC 8 或可见 http(s) URL），走 `PaneLinkActivate` | mouse.rs:849-900 |
| 右键 | pane 内容 | 打开 pane 右键菜单；若 pane 设置 right_click_passthrough 或按住 `ui.right_click_passthrough_modifier` 则转发给应用 | mouse.rs:1698-1760 |
| 右键 | pane 边框 | 始终打开 Herdr pane 菜单 | mouse.rs:1783-1791 |
| 右键 | workspace 行 / tab | workspace / tab 右键菜单 | mouse.rs:1763-1782 |
| 中键 | pane（mouse reporting） | 转发给应用 | mouse.rs:2196-2213 |
| 滚轮 | pane 内容 | 先聚焦该 pane，再转发滚轮（滚动回滚区或交给应用），行数 `ui.mouse_scroll_lines` | mouse.rs:2227-2249 |
| 滚轮 | tab 栏 | 上/下滚 = 上一/下一 tab | mouse.rs:1792-1815 |
| 滚轮 | sidebar spaces / agents 列表 | 列表滚动 | mouse.rs:1817-1848 |
| 左键拖 | pane 分割线（hit_rect） | 拖动改 split ratio，节流 33ms 发 `layout.set_split_ratio` | mouse.rs:1035-1080, 2118-2140 |
| 左键点/拖 | pane 右侧滚动条 | 跳转/拖动回滚位置 | mouse.rs:2083-2117 |
| 左键拖 | sidebar 与 pane 区分隔竖线 | 调整 sidebar 宽度；**双击（350ms 内）**恢复配置宽度 | mouse.rs:1859-1881 |
| 左键拖 | sidebar 内 spaces/agents 分隔横线 | 调整两段比例 | mouse.rs:1882-1886 |
| 左键点 | workspace 行 | 松开时聚焦 workspace；按下后拖动 ≥1 格进入 workspace 拖拽排序（linked worktree 子项不可拖） | mouse.rs:1082-1110, 1266-1275, 2031-2045 |
| 左键点 | tab | 松开时聚焦 tab；拖动 ≥1 格进入 tab 重排，松开发 `tab.move` | mouse.rs:1111-1125, 1163-1195 |
| 左键点 | agent 行 | 聚焦该 agent 所在 pane | mouse.rs:2069-2082 |
| 左键点 | ` new`（spaces 页脚左） | 新建 workspace | mouse.rs:1955-1962; sidebar.rs:365-378 |
| 左键点 | `menu`（spaces 页脚右） | 打开全局菜单 settings / keybinds / reload config / (update ready|what's new) / detach | mouse.rs:1950-1954; global_menu.rs:22-54 |
| 左键点 | ` + `（tab 栏末） | 新 tab | mouse.rs:1963-1969; tabs.rs:172-181 |
| 左键点 | ` < ` / ` > `（tab 溢出时） | 滚动 tab 条 | mouse.rs:1970-1995 |
| 左键点 | `«` / `»`（sidebar 右下角） | 折叠 / 展开 sidebar | mouse.rs:1996-2004; sidebar.rs:427-440, 154-177 |
| 左键点 | `grouped` / `priority`（agents 标题右） | 切换 agent 面板排序 | mouse.rs:1935-1949 |
| 左键点 | `▸` / `▾`（worktree 组父行末） | 折叠/展开 worktree 组 | mouse.rs:2005-2016 |
| 左键点 | toast 通知 | 跳转到通知对应 pane | mouse.rs:902-921 |
| 鼠标移动 | 菜单/navigator 行 | 高亮跟随 | mouse.rs:1298-1305 |
| shift+右键 / `ui.mouse_capture=false` | — | 绕过 Herdr 交给外层终端（原生复制粘贴） | docs/quick-start.mdx:22; docs/windows-beta.mdx:114 |

手机布局（列宽 ≤ `ui.mobile_width_threshold`，默认 64）下的额外鼠标目标：header 右侧 `switch` 按钮打开全屏 switcher；switcher 内 `close ×`、agent 行、workspace 行、tab 行、`+ new workspace`、`+ new tab`、menu 项均可点；滚轮每次 2 行（mobile.rs:757-870; src/config.rs:78）。

## C. UI 布局

### C.1 桌面默认布局（cols > 64）

布局计算在 `ClientShellConfig::layout`（src/client/shell/config.rs:343-410）：sidebar 占左侧固定列宽（默认 26，范围 18-36，src/config/model.rs:1101-1103），其余为 main；main 顶部 1 行 tab bar（`tab_bar_position` 可设 bottom），剩余为 pane surface。折叠 sidebar 时宽 4（compact，显示序号+状态点）或 0（hidden）（config.rs:363-368; model.rs:139-144）。

```
┌ sidebar (26 cols) ─────────┐┌─ tab bar (1 row) ──────────────────────────────────────────────┐
│ spaces                     ││  agents   │ logs Z │ review  │ + │            [zoom·host·12:30]│
│ ● herdr            ← focused (active_row_bg, 粗体)                                          │
│   main ↑2 ↓1       ← 第二行：branch · git_status（focused 用 mauve，否则 overlay0）          │
│ ○ api-server                                                                                │
│   feature/x                                                                                 │
│ ● docs             ▾ ← worktree 组父行，末尾 ▸/▾ 折叠开关                                     │
│   ├─ wt-a          ← 子 worktree 缩进，标签取 branch 去掉 worktree/ 前缀                      │
│   └─ wt-b                                                                                   │
│                                                                                             │
│ new                   menu ← spaces 页脚：左 " new"，右 "menu"（有更新时 "● menu"）           │
│────────────────────────────  ← spaces/agents 分隔线，可拖动（sidebar_section_split）        │
│ agents             grouped ← 右侧 grouped/priority 排序开关                                  │
│ ● herdr · agents           ← agent 行 1：state_icon workspace · tab                          │
│   claude                   ← agent 行 2：agent 名（缩进 3）                                   │
│ ○ api-server                                                                                │
│   codex                                                                                     │
│                                                                                             │
│                          « ← 右下角折叠按钮                                                  │
└────────────────────────────┘└─────────────────────────────────────────────────────────────────┘
                              ┌ claude ────────────┐┌ tests ──────────────┐
                              │ (focused: 边框+标题 accent 色、粗体)       ││ (未聚焦: overlay0)   │
                              │                    ││                     │
                              │                    │├─────────────────────┤
                              │                    ││ shell               │
                              │                   ▐││                     │  ← 右侧 1 列滚动条
                              └────────────────────┘└─────────────────────┘
                              [ PREFIX ] esc cancel  ctrl+b send prefix  w workspace nav  ? keybinds   ← mode bar（仅非 Terminal 模式时占 pane 区最底一行）
```

关键事实与证据：
- sidebar 右缘竖线 `│`（surface_dim 色）即宽度拖拽把手（sidebar.rs:190-194, 443-452）。
- spaces 标题 ` spaces`（overlay0 粗体），header 占 2 行（`WORKSPACE_HEADER_ROWS=2`，state.rs:5；sidebar.rs:199-218）。
- spaces/agents 高度按 `sidebar_section_split` 比例（0.1-0.9，最小各 3 行）分配（src/ui/sidebar.rs:30-48）。
- agents 面板：顶一行 `─` 分隔，第二行 ` agents` + 右侧排序标签 `grouped`/`priority`（agent_sidebar.rs:63-117）；agent 视图（agent.view.set）激活时标签显示视图 label 并用 accent 色。
- tab bar：focused tab 用 accent 背景 + panel 对比前景；custom_label tab 普通色；自动命名 tab DIM；zoomed tab 标签后缀 ` Z`（tabs.rs:103-119, 380-386）；溢出时两端 ` < ` ` > ` 与 `…`；末尾 ` + ` 新建（tabs.rs:63-90, 172-181）；右侧可配置 `tab_bar_right` 状态区（zoom/hostname/datetime/text/command，docs/configuration.mdx:303-322）。
- pane 边框：默认 `pane_borders=true, pane_outer_borders=true, pane_gaps=true, pane_scrollbars=true`（model.rs:1116-1119）；焦点 pane 的边框与标题用 accent 且粗体，其他 overlay0（src/ui/panes.rs:489-495, 656-664）；标题格式 ` label `（截断按显示宽度，panes.rs:25-33）；标题来源优先级：metadata title > 手动 pane 名 > （`show_agent_labels_on_pane_borders` 时）agent 显示名（src/terminal/state.rs:2112-2123）。alt screen 应用不显示滚动条（panes.rs:37-47）。
- 单 pane 时不画分割边框（panes.rs:127-128）。
- mode bar 覆盖 pane 区最底一行；tab_bar_position=bottom 时覆盖 tab 行（composition.rs:144-151）。

### C.2 sidebar 每一行显示什么

**Space（workspace）行**，默认 `rows = [["state_icon","workspace"],["branch","git_status"]]`（docs/configuration.mdx:360-365）：
- 行 1：状态图标（颜色见 D）+ 空格 + workspace 名（focused 粗体 text 色，否则 subtext0）。
- 行 2（缩进 3）：Git 分支名 · `↑ahead`（green）`↓behind`（red）；无 Git 时该行整体消失（src/ui/sidebar.rs:106-275; tokens.rs:114-160）。
- token 间分隔符 ` · `，紧跟 state_icon 后只用一个空格；宽度不够时先隐藏可变长 token（tokens.rs:174-181; sidebar.rs:158-174）。
- worktree 子项：`├─ `/`└─ ` 前缀，隐藏 branch/git 详情（sidebar.rs:639-663, 615）。
- 背景：Navigate 选中 → selection_bg；拖拽中 → surface1；focused → active_row_bg（sidebar.rs:296-302）。
- 折叠组时父行显示组内最高优先级状态（sidebar.rs:563-590）。

**Agent 行**，默认 `rows = [["state_icon","workspace","tab"],["agent"]]`：
- 行 1：状态图标 + workspace 名 · tab 名（tab 名仅在 workspace 有 >1 tab 或 tab 被手动命名时出现）。
- 行 2（缩进 3）：agent 显示名，优先级 display_agent > name（agent rename 的名字）> 检测到的 agent 标签 > metadata title（agent_sidebar.rs:210-223）。
- 可选 token：state_text（含 metadata state_labels 覆盖）、pane、terminal_title、terminal_title_stripped、`$custom`（docs/configuration.mdx:368-378）。
- focused 行背景 active_row_bg，名字 text 粗体；未聚焦名字 subtext0 粗体，状态图标 DIM（agent_sidebar.rs:265-294）。
- 排序：`spaces`（按 workspace/tab/pane 顺序）或 `priority`（状态优先级 Blocked>Done>Working>Idle>Unknown，同级按最近状态变化）（agent_sidebar.rs:20-50; shell.rs:203-212）。

**折叠 sidebar（宽 4）**：上半列 workspace 序号+状态点，下半列 agent 序号+状态点，底部 `»` 展开（sidebar.rs:25-178）。

### C.3 Zoom 模式

zoom 是 tab 级状态：焦点 pane 占满 pane surface，其他 pane 隐藏但保留布局；tab 标签加 ` Z`；`tab_bar_right` 可加 `ZOOM` pill；swap 在 zoom 下操作隐藏布局；跨 tab move 遇 zoomed 拒绝（tabs.rs:380-386; docs/socket-api.mdx:317-318, 332-333, 344-356）。

### C.4 Modal / overlay 样式

统一用居中弹窗（`centered_popup_rect`，最大留 4 列 2 行边距，src/ui/widgets.rs:161-174），header/content/footer/actions 垂直栈（widgets.rs:184-234），按钮形如 ` ↵ save `、` esc cancel `、` ^c clear `（overlays.rs:641-652）。

| overlay | 触发 | 内容 | 证据 |
|---|---|---|---|
| Onboarding | 首启 `onboarding` 缺省/true | 标题 `herdr`、副标题、三行说明（mouse-first）、`ctrl+b enters prefix mode · ? shows keybinds and settings`、` ↵ continue `；完成后写 `onboarding=false` 并打开 Settings→Integrations | src/ui/onboarding.rs:3-15; docs/configuration.mdx:35 |
| Help（keybinds） | `prefix+?` | 分组 global / navigation / workspaces·tabs / panes / custom，键名左对齐；`/` 进入过滤，Backspace/ctrl+u 编辑，↑↓/PgUp/PgDn/j/k 滚动，Esc/Enter 关闭 | src/input/keybind_help.rs:393-546; overlays.rs:976-1090 |
| Navigator（goto） | `prefix+g` | 树：workspace（◆ 标记当前）→ tab（`N panes`）→ pane（label · cwd）；`/` 搜索，`a/b/w/i/d` 过滤全部/blocked/working/idle/done，Space 展开 workspace，j/k 或 ↑↓/ctrl+n/p 移动，Enter 打开，Esc 关 | overlays.rs:672-760, 753-890; overlay_input.rs:601-758 |
| Rename | 新建/重命名 workspace/tab/pane | 单行输入框，标题 `new tab`/`rename workspace`…；Enter 保存，Esc 取消，ctrl+c 清空，ctrl+u 清行，ctrl+w/ctrl+h 删词/字 | overlay_input.rs:902-962; context_menu.rs:241, 324, 353, 394 |
| ConfirmClose | 关闭 workspace（confirm_close=true） | 标题 + detail，` ↵ confirm ` / ` esc cancel ` | overlays.rs:1109-1151 |
| Settings | `prefix+s` | 顶部 section tab：Theme / Indicators / Sound / Toast / Integrations；选项列表 `▸` 选中、` ✓` 当前；Indicators/Sound/Toast 点选即生效，Theme 需 ` ↵ apply `，Integrations ` ↵ install `；底部 ` ↑↓ select  tab section ` | settings_overlay.rs:24-31, 161-242, 308-410; mouse.rs:1411-1466 |
| ContextMenu | 右键 | 见 B.7，条目：workspace（Rename/Close/New worktree/Open worktree…/Delete worktree checkout…/Expand|Collapse）、tab（New tab/Rename/Close）、pane（Rename pane/Clear pane name/Swap with focused pane/Split right/Split down/Zoom/Send right-clicks to pane/Close pane） | context_menu.rs:4-79 |
| GlobalMenu | 点 `menu` | settings / keybinds / reload config / (update ready \| what's new) / detach | global_menu.rs:22-54 |
| ReleaseNotes / ProductAnnouncement | 更新后 | 可滚动文本，` esc close ` | overlays.rs:292-500 |
| Popup terminal | `[[keys.command]] type=popup` 或插件 popup | accent 边框、标题、默认半屏，吃掉全部输入 | composition.rs:359-391 |

Toast 卡片：3-4 行、overlay0 边框、`●` 颜色红=needs attention / 蓝=finished / accent=update；位置由 `ui.toast.herdr.position`（默认 bottom-right）决定；点击跳转（notifications.rs:123-222; model.rs:1160-1166）。复制反馈 toast：绿边框 `● copied to clipboard`，默认 bottom-center（src/ui/status.rs:15-87; model.rs:1168-1173）。配置错误横幅：黄底粗体贴右上角（status.rs:89-122）。

### C.5 手机布局（cols ≤ 64）

已内建响应式：header 2 行 + 全屏 pane，没有 sidebar 与 tab bar（config.rs:351-359）。
```
┌──────────────────────────────────────────────┐
│ ● herdr            tab agents · 1/3 │ switch ●│  ← 行1：状态点 + workspace 名 + tab 状态；右侧 10 列 "switch" 按钮，有 blocked 时角标 ×/●
│ ● 1 blocked · 2 working · 1 idle             │  ← 行2：agent 摘要（无 agent: "no agents"，全闲: "all idle"）
├──────────────────────────────────────────────┤
│ (单 pane 全屏)                                │
│                                              │
│ ● pi waiting · workspace · tab 1             │  ← 通知改为底部横幅
└──────────────────────────────────────────────┘
点 switch → Navigate 模式全屏 switcher：
│ switch                               close × │
│──────────────────────────────────────────────│
│ agents                                       │  ← 分区标题下划线
│  ● herdr            （行2：tab · working · claude）
│ spaces                                       │
│  + new workspace                             │
│  ● herdr            （行2：main · tab agents · 1/3）
│ tabs                                         │
│  + new tab                                   │
│  1 · agents                                  │
│ menu                                         │
│  settings / keybinds / reload config / detach│
```
证据：mobile.rs:50-140（header）、156-215（switch 按钮）、236-352（agent 摘要）、354-556（switcher）、559-755（items）、757-870（点击处理）。手机端 Navigate 光标上下不循环（input.rs:801-805），通知横幅见 notifications.rs:65-103。

## D. Agent 状态

### D.1 状态集合与两层模型

- **内部语义状态** `AgentState { Idle, Working, Blocked, Unknown }`（src/detect/mod.rs:11-20）。
- **对外/UI 状态** `AgentStatus { Idle, Working, Blocked, Done, Unknown }`：`Done` 不是独立检测结果，而是 `Idle && !seen` 的派生态（src/app/api_helpers.rs:96-107; docs/concepts.mdx:43-49）。
- 语义：blocked = 需要审批/回答/决策；working = 正在运行；done = 已完成但你还没看过；idle = 完成/等待且已看过；unknown = 无法置信分类（普通 shell 也是 unknown，docs/concepts.mdx:43-49; detect/mod.rs:18-19）。

### D.2 判定来源与仲裁

1. 先识别 pane 前台进程是否为 23 种已知 agent 之一（`Agent::ALL`，detect/mod.rs:43-67；`HERDR_AGENT=<agent>` 可提示包装器内的 agent，docs/agents.mdx:54）。
2. **完整生命周期 hook 集成**（Pi、OMP、Kimi、OpenCode、Kilo、MastraCode）安装且在报告时，hook 是唯一权威，不再跑屏幕规则（docs/agents.mdx:44-46; detect/mod.rs:316）。
3. 其他 agent 用 **屏幕 manifest**（TOML 规则匹配 pane 底部缓冲快照，非当前滚动视口）分类 idle/working/blocked；PTY 输出活动是 working 的常规权威，屏幕的 `visible_working` 只是诊断/回退（detect/mod.rs:22-38; docs/agents.mdx:46-48）。
4. **Blocked 判定刻意严格**：只有屏幕匹配已知的审批/提问 UI 才标 blocked；已知 agent 无规则命中则回退 idle（`default_known_agent_idle_fallback`）（docs/agents.mdx:58-62）。可见 blocker 甚至可覆盖 hook 报告的非 blocked 状态（src/terminal/state.rs:2133-2141）。
5. Working→Idle 需要防抖：连续 3 次（每 100ms 重检）确认，上限 700ms；agent 启动有 3s 宽限期（src/pane/agent_detection.rs:5-13, 40-78）。
6. 自定义/第三方 agent 可通过 `herdr pane report-agent --state idle|working|blocked|unknown` 直接报告；`--message` 描述阻塞原因（docs/integrations.mdx:66-92）。
7. `herdr agent explain <target>` 输出判定依据（docs/agents.mdx:80-87）。

### D.3 seen / Done 语义

- 状态离开 Idle 时 `seen=true`；发生"完成转换"（Working|Blocked→Idle，或 Unknown→Idle 且 agent 标签不变）时 `seen = 是否在当前活动 tab 且外层终端有焦点`——即后台完成才产生 Done（src/app/actions.rs:23-55, 1945-1965）。
- 聚焦该 tab（`mark_active_tab_seen`）、`pane focus`/`agent focus` 会把 tab 内所有 pane 标为 seen；CLI 读取不会（actions.rs:523-545; docs/cli-reference.mdx:315）。
- 外层终端失焦（OuterFocusLost）时，活动 tab 也不再抑制通知，因此用户不在看时会收到 toast/声音（actions.rs:57-62; input.rs:279-285）。

### D.4 上卷（rollup）

Workspace 状态 = 其所有 pane 中注意力优先级最高者：Blocked(4) > Idle&未看=Done(3) > Working(2) > Idle&已看(1) > Unknown(0)（src/workspace/aggregate.rs:50-75）。客户端排序/折叠组也用同一优先级（Blocked 4 > Done 3 > Working 2 > Idle 1 > Unknown 0，src/client/shell.rs:203-212）。tab 亦有 agent_status 字段供 navigator/手机 header 使用（overlays.rs:721-729; mobile.rs:104）。

### D.5 颜色与图标

| 状态 | dots（默认） | symbols（`ui.status_indicators="symbols"`） | 颜色（palette 键） |
|---|---|---|---|
| blocked | ● | × | red |
| working | ● | ◐ | yellow |
| done | ● | ✓ | teal（手机摘要首项/toast 用 blue） |
| idle | ○ | ○ | green |
| unknown | · | · | overlay0（灰） |

证据：src/client/shell.rs:182-199（图标）、229-241（颜色）；mobile.rs:307-312（done 用 blue）；notifications.rs:203-210（toast 点色 red/blue/accent）。Settings 里的选项文案 `color dots  ● ● ● ○ ·` / `distinct symbols  × ◐ ✓ ○ ·`（settings_overlay.rs:167）。图标是静态字符，无 spinner 动画（docs/configuration.mdx:345）。agent 行未聚焦时图标 DIM（agent_sidebar.rs:281-287）。

### D.6 通知方式

- 触发规则：进入 Blocked → `NeedsAttention` toast + Request 音；完成转换到 Idle → `Finished` toast + Done 音；活动 tab 且终端有焦点时抑制（Blocked 的声音不受活动 tab 抑制）（actions.rs:64-127, 208-219）。
- toast 文案：`<agent> needs attention` / `<agent> finished`，正文 `<workspace> · <序号> · <tab>`（actions.rs:196-206, 222-234）；手机横幅改写为 `<agent> waiting` / `<agent> done`（notifications.rs:73-84）。
- 延迟：`ui.toast.delay_seconds`（默认 1s）内状态回变则不发（actions.rs:2012-2043, 2057-2070）。
- 投递通道 `ui.toast.delivery`：`off`（默认）/ `herdr`（应用内卡片，可点击跳转，位置四角）/ `terminal`（OSC 通知，SSH 可用）/ `system`（OS 通知）（model.rs:1149-1157; docs/configuration.mdx:448-463; src/client/notifications.rs:9-35）。
- 声音：`Sound::Done` / `Sound::Request`，mp3，可按 agent 开关，droid 默认静音；`HERDR_DISABLE_SOUND` 全局禁用（src/sound.rs:32-36; docs/configuration.mdx:465-483）。
- 全局 attention：有 blocked agent 时手机 header 的 switch 按钮角标；sidebar 页脚 `● menu` 表示有更新（mobile.rs:196-212; sidebar.rs:379-406）。
- API：`notification.show` 可由脚本发自定义 toast（80/240 字符上限，速率限制）（docs/socket-api.mdx:358-378）。

## G. 手机端复刻难点清单与替代方案

前提事实：herdr TUI 已自带"手机布局"（cols ≤ 64：2 行 header + 全屏单 pane + 全屏 switcher，见 C.5），官方文档也明确"手机用任意 SSH 客户端即可，TUI 会自适应窄屏"（docs/how-to-work.mdx:47-58）。Web 手机端应以这套已有的窄屏交互为原型，而不是重新发明。

| # | 桌面交互 | 触屏为什么没有自然对应 | 建议的手机替代方案 | 参考做法 |
|---|---|---|---|---|
| 1 | Prefix 组合键（ctrl+b 后再按一键） | 软键盘没有 Ctrl；两段式按键无法"释放再按"；prefix 只吃一键且无超时 | 底部常驻"Herdr 按钮"（一个 ⌘ 样式 FAB），点一次进入 Prefix 状态并弹出**动作面板**（网格按钮：split ▸ / split ▾ / new tab / zoom / close / detach / ? 等），点面板项即执行；再点 FAB 或点空白退出。等价于 mode bar 的可视化。可长按 FAB 直接打开 Navigate switcher | Blink Shell 的 SmartKeys 行 + tmux 模式按钮；Termius 的自定义快捷键条 |
| 2 | Ctrl / Alt / Esc / Tab / 方向键 送入 pane（agent TUI 依赖 Esc、ctrl+c、shift+tab、↑↓） | iOS/Android 软键盘无这些键；Web 页面拿到的 key 事件与 herdr 的 `TerminalKey`+Kitty 协议编码需要完整修饰符信息（src/input/encode.rs:18-70） | 键盘上方"辅助键条"（sticky 修饰：Ctrl / Alt / Shift 可锁定一次；Esc / Tab / ↑↓←→ / ctrl+c / ctrl+d / Enter），每个键生成对应 `pane.send_keys` 键名（`esc`、`ctrl+c`、`shift+tab`…，docs/cli-reference.mdx:219-226）；长按方向键连发 | Blink Shell / Termius / a-Shell 的 accessory bar |
| 3 | 鼠标拖动分割线调 ratio（33ms 节流发 `layout.set_split_ratio`） | 触屏拖动与页面滚动/文本选择冲突；分割线只有 1 格宽 | 手机默认只显示**一个 pane**（沿用 TUI 手机布局，pane surface = 全屏），分屏通过 switcher/左右滑动切换 pane；平板/横屏才显示多 pane，并把分割线 hit 区放大到 ≥ 24px，长按分割线进入"调整模式"后拖动 | herdr 自身 mobile.rs 只渲染焦点 tab 的 surface |
| 4 | 滚轮滚回滚区 vs 应用（mouse reporting）滚动 | 触摸滚动只有一种手势；TUI agent（Claude Code 等）自己处理滚轮才能翻历史 | 双指/单指滚动默认映射为滚轮事件交给 pane（herdr 会先聚焦再转发，mouse.rs:2227-2249）；在 xterm.js 层用 `scroll.offset_from_bottom` 显示"回到底部"浮标；提供"回滚模式"开关：开启后触摸滚动改为滚 herdr 的 scrollback（对应 pane 滚动条） | Termius 的 scrollback 开关 |
| 5 | 右键菜单（pane/tab/workspace） | 无右键 | **长按** = 右键：长按 pane 弹 pane 菜单（Rename/Split/Zoom/Close…），长按 workspace/tab 行弹对应菜单；菜单改为底部 action sheet | iOS 长按上下文菜单 |
| 6 | 拖选文本 → 自动复制；双击选词；ctrl+click 打开链接 | 触屏长按被 #5 占用；系统文本选择与 canvas 终端不兼容 | 用"选择模式"按钮进入 Copy 模式（对应 `prefix+[`），显示可拖动的两个选择手柄；单击 URL 直接激活链接（走 `PaneLinkActivate`，不需 ctrl）；提供"复制最近输出/整屏"快捷项 | Blink 的 selection handles |
| 7 | 悬停高亮（menu 行、navigator 行） | 无 hover | 直接点选；用按下态代替 hover | — |
| 8 | Navigate 模式（↑↓ 选 workspace、Enter 打开、1-9 直达） | 依赖方向键与数字键 | 复用 TUI 全屏 switcher（agents / spaces / tabs / menu 四段列表），点击即聚焦；左右滑动 header 切 tab；下拉刷新 = 重新拉 `session.snapshot` | herdr mobile.rs 已有实现 |
| 9 | sidebar 常驻显示全部 agent 状态 | 屏幕太窄 | header 2 行摘要（`● 1 blocked · 2 working`）+ 有 blocked 时按钮角标；下拉抽屉或 switcher 展示完整列表；系统推送（Web Push）替代 toast | herdr mobile header |
| 10 | 键盘 Resize 模式 h/j/k/l | 无键 | 动作面板内"调整尺寸"子面板四个方向按钮；或 #3 的长按拖动 | — |
| 11 | 多客户端尺寸协商（最后交互者控制 tab 尺寸，见 F） | 手机视口小且键盘弹出会改变高度，会把桌面端 pane 缩小 | 手机端默认**只读观察**（`terminal session observe`，不参与 resize），需要输入时再 `--takeover` 或用 `pane.send_keys` API 而非成为几何控制者；或给手机客户端加"不改尺寸，仅缩放渲染"选项 | herdr 的 observe/control 两级会话（docs/persistence-remote.mdx:131-153） |
| 12 | 粘贴（bracketed paste、图片粘贴 ctrl+v 桥接） | 移动浏览器剪贴板 API 受限 | 辅助键条"粘贴"按钮读取 `navigator.clipboard`，走 `Paste` 事件（保持 bracketed）；图片走文件选择器上传后粘贴路径 | — |
| 13 | Esc 双义（herdr 各模式的退出键 vs 送入 pane） | 只有一个物理"返回"手势 | 系统返回手势 = 关闭 overlay/退出模式；辅助条上的 Esc 始终送入 pane | — |
| 14 | 双击 sidebar 分隔线复位宽度、拖 sidebar 宽度 | 手机无 sidebar | 不复刻；平板横屏可保留抽屉式 sidebar，宽度固定 | — |
| 15 | 声音/桌面通知 | 浏览器需用户手势解锁音频；后台标签页无法弹 toast | Service Worker + Web Push（订阅 `pane.agent_status_changed` 事件）；首次点击解锁音频 | — |

## H. "原汁原味"清单（Web 客户端必须复刻的核心体验点）

**P0（不做就不是 herdr）**
1. 三层容器 + agent 第四维：workspace → tab → pane（BSP 二叉分割树，方向 right/down，ratio 可调），agent 是 pane 里被识别的进程（docs/concepts.mdx:8-49; src/layout.rs:73-97）。
2. 左侧 sidebar 两段式：`spaces` 列表 + `agents` 列表，每行两行 token（状态图标 + 名字 / branch·git 或 agent 名），focused 行高亮，页脚 `new` / `menu`（C.2）。
3. 五态状态语义与颜色：blocked 红 / working 黄 / done 青 / idle 绿 / unknown 灰；dots `● ● ● ○ ·` 或 symbols `× ◐ ✓ ○ ·`；Done = idle 且未看过，看过即变 idle（D.1, D.3, D.5）。
4. 状态上卷：workspace/tab 显示内部最高优先级状态（Blocked > Done > Working > Idle > Unknown）（D.4）。
5. 点击即聚焦（pane / tab / workspace / agent 行），聚焦 tab 即标记 seen（B.7; D.3）。
6. 持久会话：关闭页面 = detach，server 与 agent 继续跑；重开即 reattach 到同一会话（docs/quick-start.mdx:54-62）。
7. 实时终端渲染 + 键入直达焦点 pane，完整修饰键（Ctrl/Alt/Shift）与 Kitty 键盘协议编码、bracketed paste（src/input/encode.rs）。
8. prefix 模式（ctrl+b 默认，可配）+ 与 TUI 一致的默认键表（B.2），mode bar 提示文案一致（B.1）。
9. 分屏：split right / split down、close pane、zoom 切换（tab 标签加 ` Z`）、focus h/j/k/l、swap、cycle（B.2, C.3）。
10. 右键菜单三套条目与文案一字不改（context_menu.rs:4-79）。
11. 通知：blocked → "X needs attention"（红点），后台完成 → "X finished"（蓝点），活动 tab 抑制，点 toast 跳转（D.6）。
12. 手机窄屏模式：2 行 header（状态点 + workspace 名 + tab 状态 + switch 按钮 + agent 摘要）+ 全屏 pane + 全屏 switcher（C.5）。

**P1（体验完整度）**
13. Navigator（goto picker，`prefix+g`）：树形 workspace/tab/pane 搜索与 a/b/w/i/d 状态过滤（C.4）。
14. keybind help（`prefix+?`）分组表 + `/` 过滤（C.4）。
15. Copy 模式（vi 键位、`/ ?` 搜索、v/y）与鼠标拖选自动复制、双击选词、复制反馈 toast（B.4, B.7）。
16. Resize 模式与拖分割线（节流 33ms，`layout.set_split_ratio`）。
17. 拖拽重排 tab 与 workspace（拖动指示线 accent 色）（B.7）。
18. Settings overlay 五段（Theme / Indicators / Sound / Toast / Integrations）与即时生效规则（C.4）。
19. 主题系统：18 个内置主题名 + `[theme.custom]` 色板覆盖 + 跟随系统明暗自动切换（E）。
20. 新建 tab 默认弹命名框（`prompt_new_tab_name=true`）、关闭 workspace 默认确认（`confirm_close=true`）（model.rs:1111-1113）。
21. sidebar 可折叠为 4 列 compact（序号 + 状态点）、宽度可拖、`hide_tab_bar_when_single_tab`、tab bar 可置底（C.1）。
22. worktree 组：父/子缩进树、折叠开关、New worktree / Open worktree… / Delete worktree checkout… 菜单（C.2; docs/configuration.mdx:95-110）。

**P2（锦上添花）**
23. `tab_bar_right` 状态区（zoom / hostname / datetime / text / command）。
24. sidebar 行 token 自定义布局（`rows`、`rows_by_agent`、`$custom` 元数据 token，最多 16×16）。
25. 插件 popup / overlay pane、agent view 声明式过滤排序（`agent.view.set`）。
26. Kitty graphics 图片显示、CJK IME 光标锚点、release notes / 更新提示 overlay。
27. 直接 attach 单个 agent 终端（`herdr agent attach`，`ctrl+b q` 退出）作为"专注模式"。

## A. 概念模型

### A.1 层级与 ID

```
Session（server 命名空间，默认 default，可命名）
 └─ Workspace  w1              ← 项目级容器，有 cwd、label、Git branch/ahead-behind、可选 worktree 组归属
     └─ Tab    w1:t1           ← 一种布局（BSP 分割树 + 焦点 pane + zoomed 标志），有 label（自动编号或自定义）
         └─ Pane w1:p1        ← 真实终端（PTY），有 label、cwd、right_click_passthrough、scroll 指标
             └─ Agent         ← pane 里被识别的前台进程（可选），有 kind、name、状态、session ref、metadata tokens
```
- 公共 ID：workspace `w1`，tab `w1:t1`，pane `w1:p1`；关闭后的 tab/pane 序号不复用；跨 workspace move 会给 pane 新 ID，旧 ID 作为别名对原进程仍可解析（skills/herdr/SKILL.md:69-78; src/app/ids.rs:15-38, 106-120; docs/cli-reference.mdx:199）。
- 服务端结构：`AppState.workspaces[ws_idx].tabs[tab_idx].layout: TileLayout(Node 二叉树)`，pane 由 `PaneId(u32)` 标识并映射到 `TerminalId`（src/layout.rs:11-97; src/workspace.rs:1097-1112）。
- 每个 pane 进程注入 `HERDR_ENV=1`、`HERDR_WORKSPACE_ID`、`HERDR_TAB_ID`、`HERDR_PANE_ID`、`HERDR_SOCKET_PATH`、`HERDR_BIN_PATH`（docs/socket-api.mdx:300-304; src/app/ids.rs:40-58）。

### A.2 生命周期与操作

| 对象 | 创建 | 支持的操作 | 销毁规则 | 证据 |
|---|---|---|---|---|
| Session | `herdr` 自动起 server；`herdr --session <name>` 命名会话 | attach/detach、`server stop`、`session list/attach/stop/delete`、reload-config、handoff | `server stop` 结束全部 pane | docs/concepts.mdx:51-79; docs/cli-reference.mdx:104-113 |
| Workspace | 空会话自动建一个；`prefix+shift+n`、sidebar ` new`、`workspace create --cwd --label --env`；worktree create/open | focus、rename、close(--group)、move（拖拽/`workspace.move`/`move_block`）、report-metadata、折叠 worktree 组 | 关闭最后一个 tab 即关闭 workspace；父 worktree 关闭需 `close_group` | docs/quick-start.mdx:16; docs/cli-reference.mdx:116-133; src/workspace.rs:1115-1126; docs/socket-api.mdx:103, 415 |
| Tab | 创建 workspace 自带首 tab + root pane；`prefix+c`、` + `、`tab create` | focus、rename、move（拖拽/`tab.move`）、close、zoom（tab 级）、`layout.export/apply` | 至少保留 1 个 tab（`close_tab` 在只剩 1 个时返回 false，由上层转为关 workspace） | src/workspace.rs:579-593; docs/cli-reference.mdx:150-163 |
| Pane | 建 tab 自带 root pane；split right/down（默认 ratio 0.5，可 `--ratio`） | focus（方向/循环/last）、swap（方向或显式）、resize（方向+amount）、zoom toggle/on/off、rename/clear、read（visible/recent/recent-unwrapped/detection）、send-text/send-keys/run、wait-output、move（到其他 tab/新 tab/新 workspace）、right-click 策略、report-agent/metadata、close | 关闭唯一 pane = 关闭 tab；唯一 tab 的唯一 pane = 关闭 workspace | docs/cli-reference.mdx:165-227; src/workspace.rs:1115-1140; src/layout.rs:143-325 |
| Agent | 在 pane 里手动运行 `claude` 等即自动检测；`agent start <name> --kind` 在空闲 shell pane 启动 | list/get/read/explain、prompt(--wait)、send-keys、wait(--until)、rename/--clear、focus、attach(--takeover)、agent.view.set | 进程退出/被替换/release 即消失，名字随之清除 | docs/cli-reference.mdx:289-321; docs/agent-automation.mdx:36-57 |

新 pane/tab/workspace 的 cwd 策略：`terminal.new_cwd = follow|home|current|<path>`，默认跟随来源 pane（docs/configuration.mdx:85-93）。创建 workspace/tab/split 默认**不抢焦点**（CLI `--no-focus` 为默认；TUI 操作则聚焦新对象）（docs/cli-reference.mdx:162; context_menu.rs:427-443 focus:true）。

### A.3 客户端与服务端

server 拥有 pane 与进程；client 只是渲染层。TUI 客户端内部持有 `session.snapshot` 的本地缓存 + `events.subscribe` 事件流，同样的组合就是 Web 客户端的引导协议（docs/socket-api.mdx:118-130; src/client/shell/state.rs snapshot 字段）。服务端渲染 pane surface（cells），客户端合成 chrome（sidebar/tab bar/overlay）后输出（composition.rs:23-180）。

## E. 配置

- 文件：Linux/macOS `~/.config/herdr/config.toml`，Windows `%APPDATA%\herdr\config.toml`；`HERDR_CONFIG_PATH` 覆盖；`herdr --default-config` 打印带注释全文（src/main.rs:64-437）；`herdr server reload-config` 或全局菜单 `reload config` 热重载（docs/configuration.mdx:10-51）。非法值回退默认并在 UI 右上角黄色横幅提示（docs/configuration.mdx:33; src/ui/status.rs:89-122）。
- 顶层节：`onboarding`、`[theme]`、`[terminal]`、`[session]`、`[server]`、`[update]`、`[keys]`、`[ui]`、`[worktrees]`、`[advanced]`、`[experimental]`、`[remote]`（src/config/model.rs:308-321）。

| 节 | 可配置项（默认） | 证据 |
|---|---|---|
| theme | `name`（18 个内置：catppuccin, catppuccin-latte, terminal, tokyo-night, tokyo-night-day, dracula, nord, gruvbox, gruvbox-light, one-dark, one-light, solarized, solarized-light, kanagawa, kanagawa-lotus, rose-pine, rose-pine-dawn, vesper；`terminal` 跟随宿主 ANSI）、`auto_switch`（false）、`light_name`/`dark_name`、`[theme.custom]` 色 token（sidebar_bg, active_row_bg, selection_bg, panel_bg, accent, text, red/green/blue/yellow/teal/mauve/peach…）、`[theme.custom.light|dark]` | src/config/theme.rs:4-22; docs/configuration.mdx:239-296 |
| keys | 见 B（prefix + 60 余个 action + `[[keys.command]]`） | model.rs:331-459 |
| ui | sidebar_width 26 / min 18 / max 36；sidebar_start_collapsed false；sidebar_collapsed_mode compact；mobile_width_threshold 64；mouse_capture true；copy_on_select true；host_cursor auto；right_click_passthrough_modifier ""；redraw_on_focus_gained true；mouse_scroll_lines 3；confirm_close true；prompt_new_tab_name true；prompt_new_workspace_name false；pane_borders/pane_outer_borders/pane_scrollbars/pane_gaps true；show_agent_labels_on_pane_borders false；hide_tab_bar_when_single_tab false；tab_bar_position top；tab_bar_right []；window_title "{hostname}: {workspace}"；agent_panel_sort spaces；status_indicators dots；accent "cyan"；`[ui.sidebar.agents|spaces]` rows/row_gap/rows_by_agent | model.rs:847-918, 1098-1134 |
| ui.toast | delivery off（herdr/terminal/system）、delay_seconds 1、herdr.position bottom-right、clipboard.enabled true、clipboard.position bottom-center | model.rs:1149-1173 |
| ui.sound | enabled、path/done_path/request_path（mp3）、`[ui.sound.agents]` 每 agent default/on/off（droid 默认 off） | docs/configuration.mdx:465-483 |
| terminal | default_shell ""（$SHELL）、shell_mode auto|login|non_login、new_cwd follow | model.rs:254-263 |
| session | resume_agents_on_restore true | model.rs:265-276 |
| server | headless_cols 120 / headless_rows 40 | model.rs:945-950 |
| update | channel stable|preview、version_check true、manifest_check true | model.rs:33-46 |
| worktrees | directory "~/.herdr/worktrees" | docs/configuration.mdx:95-104 |
| advanced | scrollback_limit_bytes 10000000 | model.rs:954-960 |
| experimental | allow_nested、kitty_graphics、pane_history、reveal_hidden_cursor_for_cjk_ime、cjk_ime_agents、cjk_ime_cursor_shape、switch_ascii_input_source_in_prefix（均 false/空） | model.rs:978-1019 |
| remote | manage_ssh_config true | model.rs:962-975 |

**不可配置**：字体（由宿主终端决定，TUI 无字体概念）；状态颜色只能通过主题色 token 间接改（red/yellow/teal/green/overlay0）。

示例（最小但覆盖 Web 需复刻的项）：
```toml
onboarding = false

[theme]
name = "catppuccin"
auto_switch = true
light_name = "catppuccin-latte"
dark_name = "catppuccin"

[theme.custom]
accent = "#89b4fa"
active_row_bg = "#1e1e2e"

[keys]
prefix = "ctrl+b"
next_tab = ["prefix+n", "ctrl+alt+]"]
switch_workspace = "prefix+shift+1..9"

[[keys.command]]
key = "prefix+alt+g"
type = "popup"
command = "lazygit"
width = "80%"
height = "80%"

[ui]
sidebar_width = 28
status_indicators = "symbols"
tab_bar_position = "top"
tab_bar_right = [{ type = "zoom" }, { type = "hostname" }, { type = "datetime", format = "%H:%M" }]

[ui.sidebar.agents]
rows = [["state_icon", "agent", "state_text"], ["workspace", "tab"]]

[ui.toast]
delivery = "herdr"
delay_seconds = 1

[ui.toast.herdr]
position = "bottom-right"

[ui.sound]
enabled = true
```

客户端本地偏好（非 config）：sidebar 宽度/折叠、spaces-agents 分割比、agent 排序、折叠的 worktree 组由客户端 `persist_chrome_preferences` 持久化（mouse.rs:1872-1880, 1946-1949; src/client/shell/preferences.rs）。`herdr --remote` 默认使用**本地**键位快照，`--remote-keybindings server` 改用远端（docs/persistence-remote.mdx:49）。

## F. 会话状态：detach / reattach / 多客户端

### F.1 Detach / Reattach
- `prefix+q`、全局菜单 `detach`、或直接关终端 = detach；server 与全部 pane 进程继续运行；再运行 `herdr` 重新 attach 到默认 session（docs/quick-start.mdx:54-62; docs/session-state.mdx:17-27）。
- 直接 attach 单 agent：`herdr agent attach <name>` / `herdr terminal attach <id>`，`ctrl+b q` 退出，`ctrl+b ctrl+b` 发送字面 ctrl+b；同一终端同时只有一个可写 owner，`--takeover` 抢占；只读观察者可多个（`terminal session observe`，输出 base64 ANSI 帧 JSON）（docs/persistence-remote.mdx:103-153）。
- 四条状态路径对比（docs/session-state.mdx:8-15）：detach/reattach 一切保留；server 重启只恢复形状（workspace/tab/pane/cwd/layout/focus）与（开启 `pane_history` 时）屏幕历史，进程重建为新 shell，支持的 agent 可用原生 session 引用 `--resume`；`--handoff` 尝试把活 PTY 移交给新 server。
- 无客户端时 server 用 120×40 虚拟尺寸布局（`[server] headless_cols/rows`，docs/configuration.mdx:53-64）。

### F.2 多客户端同时 attach 的语义
- 每个客户端有自己的 shell location（正在看的 workspace/tab），互不强制同步；服务端为每个客户端渲染其所看 tab 的 surface（src/server/headless/client_views.rs:56-93）。
- **尺寸协商是 per-tab 的"几何控制器"模型**：每个被观看的 tab 有一个 controller client；只有一个 shell client 时它控制所有 tab（`resize_tabs_for_only_shell_client`，client_views.rs:607-620）；多客户端时，最近对该 tab focus/select/interact 的客户端**claim** 控制权（`claim_shell_tab_geometry`，682-694），tab 的 PTY 尺寸随之改为该客户端的可用 pane 区域；controller 离开或不再看该 tab 时移交给仍在看的最小 client id（622-650）；非 controller 的客户端 resize 事件被忽略（711-723）。当只剩一个客户端时立即全部回归它的尺寸；无客户端时 PTY 保持最后尺寸（docs/configuration.mdx:63-64; docs/concepts.mdx:71）。
- 结论：**"最后交互的人说了算"**，其他看同一 tab 的客户端看到的是按 controller 尺寸渲染的画面（可能出现留白/裁切）。Web 客户端手机端应避免成为 controller（见 G #11）。
- 通知与 seen：`outer_terminal_focus` 是 per-client 的；toast/terminal/system 通知只发给当前前台客户端（`no_foreground_client` 原因）（docs/socket-api.mdx:378; actions.rs:57-62）。
- 协议：客户端-服务端通过稳定的 endpoint generation 协商 snapshot/screen/input/blob codec，缺失方法只禁用对应动作并本地提示，不断线（docs/socket-api.mdx:935-948）。API 客户端引导：先 `events.subscribe`，再 `session.snapshot`，回放缓冲事件（docs/socket-api.mdx:118-130；CLI `herdr api snapshot`）。
