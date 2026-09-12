# System OpenSSH 与浅色主题

本次整合 System OpenSSH 接入和暖纸色工作台，保留已有密码/密钥连接的 Go ProxyJump、移动端输入、快捷键及启动恢复行为。

- 管理员可显式设置 `HERDRX_SSH_BIN`，复用工作台服务账号的 SSH alias、跳板配置及 Kerberos/GSSAPI 身份。网站保存的凭据和该系统身份分开使用；系统模式的跳板在服务账号的 SSH 配置中设置。
- System OpenSSH 的用户名留空、端口为 `0` 时继承 SSH 配置。编辑保存保留该零值，切换认证方式会清除不再适用的网站凭据与跳板字段。
- 连接取消会终止本次创建的 OpenSSH 进程组，管道收尾有时间上限；启动前取消也会释放管道。仅断开访问，不结束远程 Herdr 和任务。
- 工作台新增 Solarized Light 暖纸色，终端新增 Solarized Light 并恢复 Catppuccin Latte；网站外观、工作台配色和终端主题分别保存。
- 暖纸色导航、弹窗的正文与辅助文字对各自背景达到至少 4.5 的对比度。终端 ANSI 淡化文字由 xterm 原生处理，不再被网站的固定前景色覆盖；增强对比遵循 xterm 的普通文字和淡化文字规则。

默认 distroless Docker 镜像不包含 OpenSSH 或 Kerberos 运行环境，也不自动启用系统身份。需要此功能时，请按 [安装说明](../install.md#system-opensshkerberos) 准备服务账号、运行环境和票据。既有 Tailcat、密码/密钥主机可按原方式使用。

本次没有数据库 schema 迁移。更新保留数据目录；回退使用兼容镜像和当前数据，不恢复旧授权状态。操作步骤见 [更新与恢复](../update-and-recovery.md)。真实 Kerberos 票据及 Mac/iOS/Android PWA 安装仍需在相应环境验收，自动化浏览器测试不能替代真机验证。
