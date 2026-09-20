# Scheduler 的 MessagePort 失效恢复补丁

`scheduler@0.27.0.patch` 处理浏览器已有 MessagePort 静默失效后，React 更新已入队、页面却停止渲染的问题。补丁由 `web/pnpm-workspace.yaml` 的 `patchedDependencies` 应用，版本和补丁摘要由锁文件固定。

## 修改范围

仅修改 `scheduler@0.27.0` 的 `cjs/scheduler.development.js` 和 `cjs/scheduler.production.js` 中使用 MessageChannel 唤醒工作循环的分支：

1. 每次发送唤醒消息，同时设置一个 250 ms 的备用定时器。消息携带递增编号，消息事件与定时器只能有一方消费该次唤醒。
2. 消息正常到达时，取消对应定时器，继续使用 MessageChannel。
3. 定时器先执行时，关闭本模块持有的两个端口，立即继续原工作循环；该模块实例后续一直使用原有的 `setTimeout(..., 0)` 调度方式，直到页面重新加载。
4. 迟到或重复的消息不能再次消费唤醒，也不能取消下一次唤醒的定时器。

补丁保留任务优先级、工作队列、取消、让出执行和异常传播规则。Node.js 的 `setImmediate` 分支及没有 MessageChannel 时的原定时器分支不变。它不替换全局 MessageChannel、不判断浏览器 UA，也不重载页面、重试登录或重放终端输入；网站与远程 Herdr 的生命周期不受此补丁控制。

## 上游依据与版本判断

WebKit 官方提交 [`67117c4`](https://github.com/WebKit/WebKit/commit/67117c4975bb291a8775a61ee3c19dfe13e50690) 处理 Networking 进程断开后的旧端口问题。其实现不是发送 `close` 事件：[`MessagePort::notifyAllConnectionsClosed`](https://github.com/WebKit/WebKit/blob/4d05d732e5a84f32675bef4cc135a2e7a9269a87/Source/WebCore/dom/MessagePort.cpp#L98-L120) 将端口标记为 detached、取消 entanglement，并移除全部事件监听器；随后 [`postMessage`](https://github.com/WebKit/WebKit/blob/4d05d732e5a84f32675bef4cc135a2e7a9269a87/Source/WebCore/dom/MessagePort.cpp#L177-L188) 可以静默返回而不投递消息。因此，监听 `close`、捕获 `postMessage` 异常均不足以恢复这条路径。

本次复现使用 Playwright 1.63.0 的 Linux WebKit 2359。其[浏览器清单](https://github.com/microsoft/playwright/blob/v1.63.0/packages/playwright-core/browsers.json)和[上游版本配置](https://github.com/microsoft/playwright/blob/v1.63.0/browser_patches/webkit/UPSTREAM_CONFIG.sh)固定到 WebKit `4d05d732e5a84f32675bef4cc135a2e7a9269a87`，已包含上述逻辑；[Playwright 补丁](https://github.com/microsoft/playwright/blob/v1.63.0/browser_patches/webkit/patches/bootstrap.diff)未修改该行为。不同平台可能使用不同 WebKit revision，不能据此宣称所有 Safari 或 Playwright 平台都已复现。

React Scheduler 初始化时创建一个 MessageChannel，并长期复用它。消息丢失后，`isMessageLoopRunning` 仍为 `true`；仅重新调用 `requestHostCallback()` 不会安排新的唤醒。同优先级的 React 更新还可能直接复用已有任务。对比 React 19.2.8 与 19.3.0 的 [Scheduler](https://github.com/facebook/react/blob/v19.3.0/packages/scheduler/src/forks/Scheduler.js#L498-L567)、[RootScheduler](https://github.com/facebook/react/blob/v19.3.0/packages/react-reconciler/src/ReactFiberRootScheduler.js#L457-L474) 和 DOM 微任务入口，该路径只有 Flow 注释、类型写法调整，没有端口失效恢复。**升级到 React 19.3 本身不能替代本补丁。**

## 时间与性能取舍

正常消息投递仍走 MessageChannel，但每次唤醒会增加一次定时器的创建与取消。250 ms 是允许备用定时器执行的延迟，并非页面恢复的时间上限：后台节流、页面冻结或主线程长期繁忙都可能延后执行。较慢的正常投递也可能触发降级，因此超时本身不被视为端口损坏的证明。

降级后，嵌套 `setTimeout(..., 0)` 可能受到浏览器约 4 ms 的最小间隔限制，降低连续调度吞吐量。选择单向降级，是为了让已失效的旧端口不再阻止更新；它只持续到当前页面结束。后台定时器仍受浏览器调度规则约束，补丁不提供后台持续执行或实时性保证。

## 验证方式

在仓库根目录执行：

```bash
pnpm --dir web install --frozen-lockfile
pnpm --dir web exec vitest run src/lib/schedulerRecovery.test.ts
make web-build
pnpm --dir web exec playwright install webkit
node scripts/test-pwa-network-recovery.mjs
```

单测针对实际安装的 development、production Scheduler 验证正常投递、端口静默失效、定时器恢复、迟到消息，以及恢复后的任务执行行为；应以测试文件中的断言和执行结果为准。

原生回归脚本只支持 Linux，启动独立浏览器和本地 HTTP 夹具。它通过 `/proc` 核对进程父子关系、启动时间和 PID，只终止自己启动的 WebKit Networking 子进程，并断言替代进程出现、旧通道停止、新通道与 HTTP 恢复。随后验证同一文档内的 200 恢复、401 退出到登录页、重新登录及后续界面操作，检查没有页面重载和登录提交重放。API 为隔离夹具，不连接用户主机或 Herdr 会话。

2026-09-21 已用同一原生脚本完成补丁前后对照：未打补丁时，网络及新通道已恢复，但页面仍停留在登录状态读取错误；打补丁后，同文档的恢复与后续操作通过。这是该 Linux WebKit 版本的回归证据，不代替完整 CI、真实设备 PWA 安装或部署验收。

## 何时移除

只有上游修复覆盖此类静默失效，或项目不再经过该调度路径，并且在**移除补丁后**通过上述单测及原生复现、相关浏览器回归，才移除 `patchedDependencies` 配置和补丁文件、重新生成锁文件。升级 React、Scheduler、Playwright 或 WebKit 时须重新核对源码和行为，不能仅依据新版本号删除补丁。若新的 WebKit 能保留旧通道或主动通知关闭，应更新原生测试对旧通道的预期，并验证 React 确实能继续调度。
