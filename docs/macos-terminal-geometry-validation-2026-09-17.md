# macOS 终端尺寸读取验收

日期：2026-09-17。代码候选为 `66fcbec8dbadd15425858c5a77b3f44c58069db2`，集成 [PR #11](https://github.com/riba2534/herdrx/pull/11) 并修复审查发现；集成入口为 [PR #12](https://github.com/riba2534/herdrx/pull/12)。

## 问题与范围

macOS 的本机与 Tailcat 接入此前调用缺省 `ReadPID`，无法为全屏应用的原生滚动读取 PTY 尺寸。新实现通过 `kern.proc.pid` 获取控制终端设备号，打开 Herdr 使用的现代 `/dev/ttysNNN`，核对打开后的字符设备类型与完整设备号，再执行只读 `TIOCGWINSZ`。没有控制终端、设备不匹配或像素无法精确还原时明确失败。

该路径不发送 resize，不修改 Herdr、pane 或任务的生命周期。基础 SSH 使用独立的 Python 程序，远端仍要求 Linux；旧式 BSD PTY 不在此次支持范围。Tailcat 远程主机需要安装包含本改动的 CLI，仅更新网站镜像不会更新远程 CLI。

## 回归修复

- 无控制终端用例主动通过 `Setsid` 脱离父会话。测试先在独立 PTY 中启动 helper，确认父进程确实拥有控制终端，避免无终端 CI 掩盖继承错误。
- 设备号拒绝用例先打开本用例的真实 slave，再只改变设备号的 major，保证相同候选路径存在且能够打开，随后验证完整设备号校验拒绝错配。
- 删除无效的 legacy 路径候选与兼容声明，保留 Open/Fstat 的具体错误。
- 原有端点集成测试扩展到 Darwin：本机和 Tailcat 使用真实 PTY、认证 SSH 通道及 Unix socket 转发，读取尺寸后确认行列和像素未改变。基础 SSH 子用例仅在 Linux 执行。

## 已执行验证

| 环境 | 验证 | 结果 |
| --- | --- | --- |
| Linux amd64 | 全量 Go vet、普通测试与 race | 通过 |
| macOS 15 arm64、amd64 | CLI 原生构建；terminalgeometry、herdr、agent 三包 vet、普通测试与 race | 两种架构均通过 |
| Linux 浏览器环境 | 前端类型检查、lint、529 个单元测试与构建；17 项浏览器门禁 | 通过 |
| Linux amd64 候选镜像 | nonroot、bind mount、初始化、静态资源、重建后持久化；Caddy HTTPS、认证 WSS、Origin 与代理限流 | 通过 |
| 隔离 CLI 发布环境 | Linux/macOS × amd64/arm64 构建、签名与附件校验、原生 CLI 冒烟、7 项发布防护回归 | 通过；使用临时测试信任根，未发布 Release |
| 独立 Herdr 0.9.0 会话 | 中文及组合输入、两阶段提交、原生单 pane/分屏滚动 | 通过；任务 PID、网格及像素保持，滚动未增加 SIGWINCH |
| 独立云工作台与另一物理主机 | 跨网 SSH、12 次刷新、桌面/手机视口同时访问、旋转、全屏滚动、旧缓存页面请求、工作台容器实际重启、退出登录 | 通过；重连使用原 pane，远程任务和尺寸保持 |

macOS 原生执行记录见 [GitHub Actions 的两种架构任务](https://github.com/riba2534/herdrx/actions/runs/35178289849)。持续门禁定义在 [ci.yml](../.github/workflows/ci.yml)，网站镜像构建与发布依赖 Linux 验证及两种 macOS 验证完成。

跨主机验证使用独立工作台容器、数据目录、账号、SSH 密钥和 Herdr 会话。SSH 转发仅承载访问连接，远程 Herdr 独立运行；测试未替换使用中的工作台，也未操作用户已有 pane。私人连接信息、原始测试记录及截图保存在不纳入 Git 的本地记录中。

## 未执行与边界

- macOS CI 验证的是内核尺寸读取和接入协议，不代表 Intel Mac 完整用户环境或 launchd 服务生命周期已验收，受控端仍为预览。
- 本轮跨网工作台与远程 Herdr 均为 Linux；没有把它作为 macOS 跨网实机或 Tailcat WireGuard/DERP 链路的新增验收。
- 浏览器手机视口测试不等于实体 Mac/iOS/Android PWA 安装、系统输入法或真实触摸手感验收。
- 未执行 72 小时运行、整机断电及并发原生窗口调整的专项验证。尺寸读取与控制握手之间仍存在既有的非原子窗口，详见[滚动验收边界](flicker-mobile-validation-2026-09-06.md)。
