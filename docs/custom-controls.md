# 自绘控件与提示

应用内的下拉选择、操作确认、日期时间选择、悬停提示及表单校验统一使用自绘界面，跟随网站或工作台的浅色／深色外观。

| 场景 | 统一入口 | 行为 |
|---|---|---|
| 主机接入、SSH 认证、密钥、文件夹、角色、状态、显示与终端主题 | `Select` / `SelectOption` | 方向键、Home/End、文字定位、Enter 选择、Escape 收起；长列表滚动、窄屏避让 |
| 删除主机、关闭 Pane/标签页/工作区/Worktree、访问管理、打开链接 | `useConfirm` / `Modal` | 显示对象与后果，默认聚焦取消；取消或卸载不执行操作，切换主机取消旧确认 |
| 弹窗 | `Modal` | 焦点保持与返回、最上层 Escape、忙碌时防止关闭、适配软键盘后的可见高度 |
| 截断文本与图标操作提示 | `Tooltips` + `data-tooltip` | 项目样式绘制，鼠标悬停或键盘聚焦显示，Escape 收起 |
| 登录、注册、主机、密钥和文件夹表单 | `Form` + `Input` / `Textarea` / `Field` | 内联显示必填、邮箱、长度、数值范围错误；聚焦首个错误，修正时保留输入 |
| 审计日期时间 | `DateTimeField` | 自绘日历、年月菜单和时分输入；应用后更新条件，以浏览器本地时区转换 |

新功能不要直接使用原生 `<select>`、日期选择输入、DOM `title` 或 `window.alert/confirm/prompt`。表单统一经过 `Form`，不触发浏览器验证气泡。无样式 Radix primitives 负责弹层、选择与焦点交互，外观由 `web/src/controls.css` 和既有主题变量定义；内部隐藏的表单兼容元素不呈现选择界面。

文件选取、通知授权和密码管理器等浏览器／操作系统界面由平台提供，网页不替代这些平台界面。

## 验证记录（2026-09-07）

- 75 项前端测试通过，包含确认取消、只执行一次、切换主机／卸载取消、受控输入修正、外部链接确认与终端实例保持。
- Chromium、Firefox、WebKit 中通过自绘菜单、嵌套 Escape、焦点返回、长列表、空选项、表单校验、操作请求次数、日历过滤、浅深主题及 1440/390/320 px 检查。
- 三种引擎的主机／密钥／文件夹、Tailcat 安装引导、网站外观、工作台显示回归通过；隔离网站二进制的登录／访问管理测试通过。
- 终端排版、重绘、历史切换及手机滚动检查通过。手机软键盘使用 visualViewport 事件验证，WebKit 触摸部分使用合成 TouchEvent；不等同于实体手机验收。
- TypeScript、lint、前端与网站构建通过。未改变后端协议、数据库结构或 CLI 安装发布逻辑。

```bash
pnpm --dir web exec vitest run
make web-build
HERDRX_TEST_ENGINES=chromium,firefox,webkit node scripts/test-custom-controls.mjs
```

自绘控件浏览器脚本已接入 CI，并检查源码中是否重新引入原生下拉、提示或绕过统一校验的表单。首次远端 CI 执行与公开源码发布仍按发布清单单独记录。
