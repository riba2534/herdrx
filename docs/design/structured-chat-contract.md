# 结构化对话记录契约（structured chat contract）

日期：2026-09-13。状态：**已冻结，待实现**。本文是实现前的契约，不代表已实现或已验收。

本契约定义 Chat 视图真正需要的**带 role 的逐轮问答数据层**：数据来自 Agent 自己写的
append-only 会话日志，经 owner 认证的 HTTP 端点按游标增量读取，前端按 role 分列渲染。
类型与常量的唯一正文是 [`web/src/lib/structuredChatTypes.ts`](../../web/src/lib/structuredChatTypes.ts)；
本文只解释语义，不重复字段定义。

现有 Chat 视图（`web/src/components/ChatView.tsx`）渲染的是"本页内存用户气泡 + 整块
`pane.read` 终端文本"，没有角色、没有轮次、没有增量。**本文定义的契约就是替换那整块终端文本的
数据源。** 在契约落地之前，现状不被视为满足要求。

---

## 1. 为什么要新增能力，而不是复用 Herdr 协议

- `herdr.sock` 的 JSON API 里只有 `pane.read`（无角色的屏幕 / 滚动文本）与 `agent.*`
  （控制类：prompt / wait / send_keys），**没有任何返回带 role 消息数组的 schema**。
- WebSocket 的 `call` 透传路径（`internal/httpapi/workbench.go` 的 `allowedMethod`）只做方法名
  白名单，**零参数校验**，`method` 与 `params` 原样交给 `endpoint.Call`。把文件读取接进这条路径
  等于开放路径穿越，因此**禁止**把它加入 `allowedMethod`。
- 因此读取能力走**独立的、owner 认证的 HTTP 路由**，与 `/panes/{paneID}/paste-image` 同级。

**关于"协议不支持"的推理纪律（纠正）。** 早期结论用「生产代码只调用过 `recent+ansi`
与 `visible+text`」推断「`recent+text` 协议不支持」。这是无效推理：**没有调用点只能证明我们
没验证过，不能证明协议拒绝它。** 由此得到两条约束：

1. 不得以"某形状没有调用点"否定任何协议形状；要使用某形状，必须做**运行时能力探测**并如实记录结果。
2. 本契约的 Chat 数据路径**完全不依赖 `pane.read`**，其形状支持与否都不影响本契约的成立。

---

## 2. 端点（冻结）

```
GET /api/hosts/{hostID}/panes/{paneID}/transcript
      ?session=<opaqueCandidateId>&cursor=<opaqueCursor>&before=<opaqueBefore>
```

- 挂在 `internal/httpapi/hosts.go` 的 `hostRoutes` 内、`authenticate` 之后，沿用 `ownedHost`
  owner 校验；非 owner 一律 404。
- 客户端**只传** `hostID` / `paneID` / 三个不透明参数。**绝不接受** agent、cwd 或路径。
- 真实 `pane.agent` 与 cwd 由服务端调 `endpoint.Snapshot(ctx)` 现取（`Pane.Agent` /
  `Pane.ForegroundCWD`，回退 `Pane.CWD`）。取不到就是 `cwd_unavailable`，不比目录名、不猜。
- 降级情形同样返回 **HTTP 200**，形状见 §4；只有这样前端才能渲染诚实的空态而不是网络错误。

HTTP 错误码仅保留真正的传输 / 权限失败：404 `host_not_found` / `pane_not_found`，
502 `transcript_unavailable`。业务性降级一律走 200 + `reason`。

---

## 3. 候选与显式绑定（纠正的核心）

### 3.1 无 `session` 时只列候选

无 `session` 参数时，服务端返回 `candidates`，`messages: []`。候选集合已被
**当前 pane 的 agent** 与**精确 cwd** 两个条件限定，每个候选只含
`{id, agent, session_id, updated_at}`：

- **不返回完整文件路径**，也不返回 cwd 原文；
- **不返回任何用户 prompt 预览**；
- 候选列表本身不构成对会话内容的读取。

### 3.2 禁止自动绑定（纠正）

**禁止"取 cwd 对应目录下 mtime 最新的文件"作为自动绑定。** 即使候选集合里只剩一个文件，
服务端也**不得**自动认领：`messages` 保持 `[]`，由用户在列表里显式选择。

- 用户操作文案固定为 **"选择此终端的会话记录"**。不要写成"已自动识别""检测到会话"这类
  暗示服务端已经确定归属的措辞。
- 选择不是任意路径权限：`session` 只是一个不透明 id，服务端把它解析回自己已经认定过的
  那一个文件；客户端无法用它指向别处。
- 自动绑定 `binding: 'verified'` 在契约中预留，**本期不得产生**。它要求严格且已验证的
  pane/provider 会话证据（例如 agent hook 上报属于该 pane 的 session_id + transcript_path）。
  herdrx 目前没有这类证据源，凭空返回 `verified` 就是臆造绑定。

### 3.3 目录名编码不能证明 cwd 归属（纠正）

Claude 的日志目录名编码规则是「每个非字母数字字符替换为一个 `-`，**不折叠连续符号**」
（所以 `/.claude` 编码成 `--claude`）。这套编码是**有损**的：`/a-b` 与 `/a/b` 编码结果相同。
因此：

- 编码前缀（即便做了 `-` 边界比较，只挡得住 `…/orca` 认领 `…/orca-secret` 这类兄弟前缀）
  **只能用于缩小搜索范围**，绝不能作为 cwd 归属的证明；
- 归属判定必须落到记录内容：读文件头若干行，要求其中至少一条记录的真实 cwd 字段
  （Claude 的 `record.cwd`、Codex 的 `session_meta` cwd）与 pane cwd **精确相等**；
- 即便如此，这也只是"候选资格"，不是自动绑定——仍须用户在列表里显式选择（§3.2）。

---

## 4. 响应状态机

| 情形 | 形状 |
|---|---|
| 不支持（agent / transport / cwd / 日志根 / 内部错误） | `supported:false` + `reason` + `messages:[]` |
| supported，无 `session` | `supported:true` + `candidates` + `messages:[]`；候选为空时 `reason:'no_session_candidates'` |
| supported，`session` 失效 | `supported:true` + `reason:'session_unavailable'` + 重新给出的 `candidates` + `messages:[]` + `reset:true` |
| supported，已绑定 | `supported:true` + `messages` + `agent` + `session_id` + `binding:'selected'` + 游标 |

`reason` 取值是闭集合，见类型文件 `CHAT_REASONS`。文件为空时 `messages: []` 且无 `reason`——
那是正常的空会话，不是降级。

**任何情形下都不得用终端文本、屏幕快照或启发式推断填充 `messages`。** 没有权威数据就返回
`supported:false` 或空 `messages`，不允许退回假回答。

---

## 5. 记录、块与角色

- 记录：`{id, role, at?, blocks}`；块只有三种：`text`、`tool-call`、`tool-result`。
- 角色闭集合：`user | assistant | tool | system`。**没有 `reasoning`**——provider 记录里的
  思考过程 / 思想链在服务端直接省略，**不计入 `skipped`**（省略是设计行为，不是格式畸形）。
- **`tool` 是派生角色**：provider 里 `type:'user'` 且 blocks 全部是 `tool-result` 的记录重判为
  `tool`。因此**工具结果永远不会被当成用户问题**渲染到右侧；只要该 user 记录里还夹着任何非
  tool-result 块，它就仍然是 `user`。
- 不输出图片引用、编辑补丁、token 用量、请求 id、原始 provider 帧等 provider 内部字段。

### 5.2 记录 id 与去重（纠正）

| Agent | id 规则 | 重复时 |
|---|---|---|
| Claude | source record 的 `uuid` | 同 uuid = 同一条记录被重发（内容可能已更新）→ **原地更新**，不丢弃、不追加第二条 |
| Codex | `<session_id>:<源文件字节 offset>` | offset 天然唯一 → 同源重复内容得到两条记录，**两条都保留** |

**禁止用 `role + 归一化文本` 的 hash 充当 id 或同源去重键。** 那样会把同源出现的两次相同
prompt 吞成一条，正好丢掉用户真实问过的第二次。跨源合并（将来若加入 hook / scrape）才允许按
turn 合并，且必须显式比较来源优先级；本期只有单一磁盘源，不存在跨源合并。

### 5.3 Codex 双记形态（`event_msg` / `response_item`）

同一份 Codex 会话文件可能同时出现 `response_item` 与 `event_msg` 两种记录形状，描述同一个
轮次。处理规则：

- **按明确类型选择一套规范消息路径，不做全局文本去重。** 规范路径按**文件**判定：
  该文件存在任何 `response_item` 帧时，消息一律走 `response_item`，`event_msg` 中的
  消息型 payload（`user_message` / `agent_message` / `item_completed`）全部忽略；
  该文件完全没有 `response_item` 帧时才走 `event_msg`。
- `event_msg` 的生命周期信号（`turn_aborted` → `system` 记录）在任何情况下都保留。
- 旧式「未包裹的 `response_item`」形态必须与包裹形态一并处理；只处理一种会在部分 Codex
  版本上得到空会话。

> ⚠️ 本条的具体取舍（哪种 payload 走哪条路径）**未经真实日志验证**，见 §11。

---

## 6. 游标、增量、分页

游标是**不透明字符串**，由服务端生成，内部绑定「候选 id（即服务端认定的文件）+
文件身份 + 字节偏移」。客户端不得解析、拼接或持久化成路径。

- **正向**：带 `cursor` 取新增记录，返回新的 `next_cursor`。这是轮询的唯一方式，
  不做全量重读。
- **反向**：带 `before` 取更早一页，返回 `previous_cursor`。
- **首屏不拉全量**：无游标时只返回日志尾部 `STRUCTURED_CHAT_INITIAL_WINDOW_BYTES`
  并按行对齐，`previous_cursor` 指向上一个窗口，供"加载更早"。
- **`has_more`** 相对本次请求方向：正向请求表示还有更新记录，反向请求表示还有更早记录。
- **单次上限**：记录数（默认 200 / 硬上限 500）与响应字节数（256 KiB）双上限。触顶时
  **停在最后一条完整记录之后**，置 `has_more:true`，`next_cursor` 指向该处；客户端立刻再取
  一页。绝不为了凑上限而截断成半条记录。

### 6.1 半行

**尾部未以换行结尾的行是"尚未写完"的行，服务端不解析、不产出记录、游标也不推进**，
下次续读时整行重读。这与"整轮落盘才有结构视图"的 provider 行为一致：流式期间结构视图是
静默的，UI 用 `pane.agent_status` 显示"正在等待 Agent 完成本轮回复"并给出切回终端的入口。

### 6.2 rotation / 截断 / 身份变化

下列任一情况成立时返回 `reset: true`，服务端从尾部窗口重新读取，客户端**整体清空重建**：

- 游标指向的偏移大于当前文件大小（文件被截断或轮转）；
- 文件身份（inode / device）与游标内记录的不一致（被替换成另一个文件）；
- 游标未落在行边界（`file[cursor-1] !== '\n'`）——服务端回退到前一个换行符再读，
  即使因此重复投递，也由"按 id 原地更新"安全吸收；
- 请求里的 `session` 与解析出的 `session_id` 不再对应同一个文件。

---

## 7. 当前端不支持时：文案与行为

一律**不渲染 `pane.read` 文本**，一律保留完整终端视图作为出口：

| 情形 | 文案要点 |
|---|---|
| `unsupported_agent` | 该终端运行的 `<agent>` 暂无结构化会话记录，可切回终端查看完整界面 |
| `no_agent` | 这是普通 Shell，没有对话记录；发送内容会作为命令执行 |
| `unsupported_transport` | 当前接入方式暂不支持读取会话记录，已保留完整终端视图 |
| `cwd_unavailable` / `log_root_unavailable` | 暂时无法定位这个终端的会话记录；已开始自动重试 |
| `no_session_candidates` | 尚未找到这个终端的会话记录（Agent 刚启动时可能还没写盘） |
| `session_unavailable` | 该会话记录已失效，请重新选择 |
| `skipped > 0` | 顶部弱化提示：有 N 条记录格式无法识别，可能未完整显示 |

错误文案必须说明用户下一步可执行的动作，不能推荐未实现的命令。

---

## 8. 受限文件读取的信任边界

这是本契约唯一新增的能力，必须显式约束，且**只经 owner 认证的 HTTP**：

1. **客户端永不提供路径**：路径只能由「agent 白名单表 + 服务端自己取到的 pane cwd」推导。
2. **日志根固定且只有两个**：Claude 的 `<远端 $HOME>/.claude/projects/` 与 Codex 的
   `<远端 $HOME>/.codex/sessions/`。`$HOME` 用一次受限命令取回，并要求以 `/` 开头且非空。
   不使用通配（如 `~/.claude/projects/*`）跨目录收集。
3. **路径逃逸拒绝**：拼接后 `Clean`，必须是白名单根的严格子路径；拒绝任何含 `..` 的段；
   `lstat` 拒绝符号链接（本地读用 `O_NOFOLLOW`；远端先判 `-L` 再读）。
4. **文件名与深度严格**：Claude 只收 `<编码目录>/<uuid>.jsonl` 直属文件；
   Codex 只收 `sessions` 下的 `rollout-*.jsonl`。不做递归通配。
5. **读取上限**：单次响应 ≤ 256 KiB，单块 ≤ 64 KiB，游标必须落在 `0 ≤ cursor ≤ fileSize`
   且行对齐。远端命令等价于 `tail -c +<N> | head -c <cap>`。
6. **owner 校验**：沿用 `ownedHost`；非 owner 返回 404，不泄露 host 是否存在。
7. **审计**：每次打开会话写一条 `transcript.opened`（含 pane_id / agent / session_id），
   越界拒绝写一条 `transcript.denied`。
8. **不进 RPC 白名单**：见 §1。

进程内和远端实现共用同一套语义；Tailcat 复用 SSH 通道时自动获得同样能力，不额外放权。

---

## 9. 与现有 UI 的兼容

- **`ChatViewProps` 接口保持不变**：
  `{ hostID, pane, client, compact, connected, submit, onSwitchToTerminal, onPasteImages?, onFocus? }`。
  `WorkbenchPage.tsx` / `TerminalPane.tsx` / `Composer.tsx` 因此**无需改动**，不产生跨路冲突。
- 终端覆盖机制（不卸载 xterm、`disableStdin`、覆盖期不 resize、`aria-hidden`、继续 ack）保留。
- `Composer` 保持 `variant='chat'`，Enter 发送 / Shift+Enter 换行 / IME 防护由既有实现负责，
  **不另建输入组件**。
- 视图模式持久化 `paneViewMode.ts` 保留；默认视图仍是终端，用户不主动切换时行为零变化。
- 面板切换控件是单个 `role="switch"` 的开关（Terminal ⇄ Chat），不是分段控件。

---

## 10. 并行实现的文件归属（互斥）

| 归属 | 文件 |
|---|---|
| **契约（已冻结）** | `web/src/lib/structuredChatTypes.ts`、`docs/design/structured-chat-contract.md` |
| 后端 | `internal/agentlog/**`（新建）、`internal/herdr/transcript.go`、`internal/herdr/transcript_local.go`、`internal/herdr/transcript_ssh.go`、`internal/httpapi/transcript.go` + 测试；`internal/httpapi/hosts.go` 仅加 1 行路由 |
| 前端数据层 | `web/src/lib/structuredChat.ts` + 测试、`web/src/lib/structuredChatMarkdown.ts` + 测试 |
| 前端 UI | `web/src/components/ChatView.tsx`、`web/src/components/ChatView.css`、`web/src/components/ChatView.test.tsx`、`web/src/components/chat/**` |
| 验收脚本 | `scripts/test-structured-chat.mjs` |

后端**禁止**改 `internal/herdr/types.go` 的 `Endpoint`（已有三套实现与大量测试桩），
新能力用独立接口。前端数据层**禁止**改 `web/src/components/**`；UI 层**禁止**改 `web/src/lib/structuredChat*.ts`。

---

## 11. 未验证 / 本期明确不可用

本契约在**没有读取任何真实用户会话记录**的前提下写成，因此下列各点**未经真实数据验证**，
不得当作已支持：

1. **Codex 双记形态的规范路径取舍**（§5.3）——需要真实 `~/.codex/sessions` 样本才能定案。
   在验证之前，遇到无法确定的形状一律计入 `skipped`，不得猜测角色。
2. **Claude / Codex 记录字段名对已安装 CLI 版本的适应性**——provider 改字段会**静默**丢消息
   （decoder 大量 `return null`），`skipped` 计数就是给这种静默准备的对策。
3. **`system_ssh`（OpenSSH ControlMaster）通道上的远端受限读取**——代码存在但全仓库未被调用过，
   必须先做能力探测；不可用则该 transport 返回 `supported:false`，**不假装支持**。
4. **`session.snapshot` 是否稳定提供 `foreground_cwd`**——字段存在，但对无头 / 前台切换场景的
   实际填充未逐例核验；为空时回退 `cwd`，两者都空即 `cwd_unavailable`。
5. **候选 id 的派生方式**——只约定"不透明、服务端派生、不含路径、不可伪造"，具体算法由实现决定。

在这些点被真实隔离会话验证之前，正确行为是返回 §4 的降级形状，而不是返回看起来合理的假记录。

---

## 12. 验收要点

1. **单元（不连真实 Herdr、不碰用户会话）**：用 fixture 的 Claude / Codex JSONL 断言
   role 派生（全 tool-result 的 user 记录 → `tool`）、tool-call 与 tool-result 按 id 配对、
   cwd 不匹配不算候选、编码碰撞（`/a-b` vs `/a/b`）被拒、符号链接被拒、单次上限、
   半行不消费且下次续读、`skipped` 计数、Codex 两种记录形状各一份。
2. **端点**：非 owner 404；未知 agent 返回 `supported:false`；第二次带 `cursor` 只返回增量；
   `reset` 分支；审计条目落库；**无 `session` 时不返回任何消息**。
3. **前端**：`user` 在右、`assistant` 在左、工具卡片可折叠且显示真实工具名、
   同源重复 prompt 渲染成两条、同 uuid 重发只更新不重复、`reset` 清空重建、
   `ChatView` **从不调用 `pane.read`**。
4. **隔离真实会话**：全新 session / 端口 / 临时目录，两个 pane 指向不同 cwd，
   断言互不串读；篡改 `paneID` 或越界 `session` 返回 403 / `read_denied`。
5. 记录写入已忽略的 `.local-notes/`，标注日期与未验证项；**不提交**、不改线上部署、不动数据目录。
