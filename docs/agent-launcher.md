# 启动程序与标签计时

标签栏的 **+** 用来在当前工作区新建标签页，并直接启动一个程序。这对应官方 Herdr 应用标签栏的 + 菜单：从这里启动 Claude Code、Codex 或任意程序，自定义命令会出现在同一个菜单里，打开菜单时可以用 1 到 9 选择前九项。

依据：

- [Download · Herdr](https://herdr.dev/download/)：从标签栏的 + 启动 Claude Code、Codex 或任意程序。
- [herdr-app CHANGELOG 0.1.8](https://github.com/herdrdev/herdr-app/blob/master/CHANGELOG.md)：`programs.json` 里的自定义命令进入 + 菜单，包括 shell 函数。
- [herdr-app CHANGELOG 0.1.10](https://github.com/herdrdev/herdr-app/blob/master/CHANGELOG.md)：菜单打开时用 1 到 9 启动；标签上会显示时间。
- [defaults/programs.json](https://github.com/herdrdev/herdr-app/blob/master/defaults/programs.json)：内置命令的 argv。herdrx 采用其中 Claude Code（`claude`）、Codex（`codex`）、Gemini（`gemini`）、Cursor（`cursor-agent`）、Pi（`pi`）、Grok（`grok`）、Antigravity（`agy`）。

## 怎么用

电脑上点标签栏右侧的 **+**，或按 Ctrl+B C。手机没有标签栏，在「切换工作区或终端」里点「启动程序」。菜单打开时按 1 到 9 选择前九项。

选「空终端」只新建标签页。选其他项会在新标签页的 shell 里输入那一行命令并回车。命令在远程主机的登录 shell 里执行，所以 shell 函数和 `PATH` 里的程序可以用。命令不会在工作台主机上运行。

发送失败时不自动重试：输入有可能已经进了终端。请先看新标签页，不要再点一次，以免执行两次。

标签显示「这一轮已工作」或「等待确认」的时间。Herdr 的状态里没有开始时刻，所以时间从**这个浏览器页第一次看到这一轮**算起；刷新后同一轮会接着计，关掉页面再打开则从再次看到时重计。不足 5 秒不显示。空闲和已完成不显示计时。

## 自定义启动项

官方应用把可改的程序写在本机 `~/.config/herdr-app/programs.json`，同一台机器上的 Agent 直接改文件（见 [herdr-app docs/security.md](https://github.com/herdrdev/herdr-app/blob/master/docs/security.md) 里的 app home）。herdrx 的浏览器、工作台主机和远程主机不是同一台机器，自定义项跟**账号**走，每台主机的菜单都用这一份。

在「编辑启动项」里添加，或让 Agent 只输出下面这种 JSON，再粘贴导入。导入会替换现有自定义项，内置项不受影响。

```json
{"launchers":[{"id":"review","name":"审查","command":"codex review"}]}
```

- `id`：小写字母开头，只含小写字母、数字和连字符，最多 32 个字符。不能用 `shell`、`claude`、`codex`、`gemini`、`cursor`、`pi`、`grok`、`agy`。
- `name`：菜单上的名字，1 到 32 个字符。
- `command`：一行 shell 命令，1 到 200 个字符，不能含换行或控制字符。
- 最多 24 项。

点菜单项等于在远程终端里执行这一行。只添加你愿意自己运行的命令。

## 没有照搬的部分

这些是官方应用里核对过、但 herdrx 已经有或与当前架构不合的能力：

| 官方应用 | 处理 |
|---|---|
| herdr-core：关掉窗口后终端继续跑（[herdr.dev](https://herdr.dev/)） | 已有。远程 Herdr 不依赖浏览器或网站进程。 |
| SSH 把其他机器放进同一个侧栏（[herdr.dev](https://herdr.dev/)） | 已有 SSH 与 Tailcat 接入。 |
| Agent 状态：工作中、等待你、已完成（[herdr.dev](https://herdr.dev/)） | 已有 Agent 总览和标签状态点。 |
| 应用自更新（[Download · Herdr](https://herdr.dev/download/)） | 网站用镜像更新，见 [更新与恢复](update-and-recovery.md)。网站不升级远程 Herdr。 |
| 布局、mods、主题都是 Agent 可改的 JavaScript（[herdr.dev](https://herdr.dev/) 与 [docs/security.md](https://github.com/herdrdev/herdr-app/blob/master/docs/security.md)） | 未引入。mod 是带用户权限的不受沙箱 JavaScript，不适合多用户 Web 工作台。 |
| `herdr-app --skill` 让 Agent 驱动窗口（[herdr-app README](https://github.com/herdrdev/herdr-app/blob/master/README.md)） | 未引入。工作台不是本机窗口进程。 |
