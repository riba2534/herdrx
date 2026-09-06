# 图片粘贴修复与验证

日期：2026-09-05。

## 复现与原因

在真实局域网 HTTP 工作台内向临时 Pane 粘贴 PNG，原实现返回 HTTP 400、`invalid_pane_id`，没有终端输入。

1. 真实 Pane ID 包含冒号，例如 `w27:p1`。前端 `encodeURIComponent` 生成 `w27%3Ap1`，chi 路由使用 `URL.RawPath` 时会保留这个编码。上传接口直接用 ID 正则校验编码字符串，因此拒绝了实际用户的所有此类请求。
2. 原测试仅使用 `p1`，成功路径还设置了 `inject=false`，没有覆盖真实 ID 或终端投递。
3. 投递逻辑使用 `pane.send_text` 并在路径后添加空格。该方法是原始按键输入，不能稳定表达一次图片路径粘贴。Herdr 原生图片桥接会依据目标程序的 bracketed-paste 设置编码粘贴路径。

## 改动

- 仅在路由确实使用 `RawPath` 时将 Pane ID 解码一次，再校验。二次编码、路径分隔符、NUL 均被拒绝。
- 上传完成后通过 `pane.send_input` 发送完整路径、空 keys，让 Herdr 按目标程序当前模式编码粘贴。不添加尾部空格或 Enter。
- 原版 TUI 上传后使用该 TUI 的终端流发送粘贴，避免把图片送入外层 Web Pane 而不是 TUI 内选择的 Pane；流已切换时报告错误。
- 前端统一原生 paste 拦截，优先采用事件实际目标 Pane，仅在无具体终端目标时使用活动 Pane。对话框、搜索框和富文本输入不被劫持。
- 多图上传串行执行；新增文件选择入口、可见进度、持久失败提示与重试。

## 验证

- HTTP 集成回归测试：通过真实 HTTP multipart 请求和本地 Unix socket 测试端点，检查编码 ID、图片落盘字节、`pane.send_input`、准确目标与无 Enter 的完整路径。
- 前端回归测试：纯文本透传、图片只处理一次、超限拒绝、多个 Pane 目标、文件选择、失败重试、多图顺序及对话框不劫持。
- 真实 Chromium 在隔离的局域网 HTTP 测试实例上使用原生 Ctrl+V：页面为非安全上下文且 `navigator.clipboard` 未提供，粘贴仍成功。测试图片从独立本机测试页面通过用户手势写入浏览器剪贴板。
- 临时 PTY 接收程序实际收到 `ESC[200~<图片路径>ESC[201~`，落盘文件是 16×16 PNG，上传按钮也投递了第二次完整粘贴。
- 在同一临时 Pane 启动 Codex，仅测试输入界面，未提交任务。Ctrl+V 后实际显示 `[Image #1]` 附件。
- Go 测试、Go vet、TypeScript 构建、前端 lint 和 14 项前端测试通过。

本轮端到端验证为本机 Herdr；SSH/tailcat 共用修正后的上传路由和投递方法，未另行验证远程实例。所有运行测试限于临时 workspace。
