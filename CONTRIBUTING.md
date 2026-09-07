# 参与 herdrx

欢迎提交可复现的问题、文档修正和功能改进。涉及新的接入方式、权限模型或任务生命周期时，请先通过 Issue 说明使用场景。

## 本地开发

使用 Go 1.27.1、Node.js 26.4.0 和 pnpm 11.25.0：

```bash
pnpm --dir web install --frozen-lockfile
make web-build
go run ./cmd/herdrx-server
```

网站默认位于 `http://127.0.0.1:8080`，运行数据写入 `./data`。前端修改后重新执行 `make web-build`。只构建远程 CLI 时可直接运行 `go build -o bin/herdrx ./cmd/herdrx`，无需 Node.js。

## 目录约定

| 目录 | 内容 |
|---|---|
| `cmd/` | 网站、CLI 和旧命令兼容入口 |
| `internal/` | Go 实现及测试 |
| `web/src/` | React、TypeScript 和终端界面 |
| `web/public/` | 随网站分发的品牌、PWA 和静态资源 |
| `deploy/`、`.github/workflows/` | 部署文件与流水线 |
| `scripts/` | 构建、发布及隔离验收工具 |
| `docs/`、`design-system/` | 使用说明、设计决策、验收范围与品牌原稿 |

`bin/`、`web/dist/`、`internal/webassets/dist/` 的构建内容，依赖缓存、数据库、环境配置和测试输出均不进入 Git；embed 目录仅保留 `.gitkeep`，由构建命令填充。个人运行记录放在已忽略的 `.local-notes/`。不要提交私钥、绑定凭据、终端实录、私人镜像地址或真实用户数据。

## 验证改动

按改动选择相关检查，发布前执行完整流水线：

```bash
go vet ./...
go test -count=1 -timeout=10m ./...
go test -race -count=1 -timeout=10m ./...
pnpm --dir web typecheck
pnpm --dir web lint
pnpm --dir web exec vitest run
python3 scripts/check-deployment.py
```

浏览器脚本位于 `scripts/test-*.mjs`，运行前构建前端，并用 `pnpm --dir web exec playwright install chromium firefox webkit` 安装对应浏览器。真实网站认证测试还需传入网站二进制路径。完整命令见 [CLAUDE.md](CLAUDE.md)，CI 定义见 [ci.yml](.github/workflows/ci.yml)。

测试使用独立端口、临时数据目录和专用 Herdr 工作区。关闭网页、撤销登录、重启网站及断网只应断开访问，不能停止远程 Herdr、删除 pane 或重放不确定的输入。不要用正在工作的终端做故障测试。

依赖变化后，重新执行 `python3 scripts/build-dependency-notices.py` 并核对第三方许可与 SBOM。发布脚本和维护流程见 [CLI 发布](docs/cli-release.md) 与 [镜像发布](docs/deployment.md)。

## 提交问题和 Pull Request

问题报告请包含版本、工作台与远程主机的系统/架构、接入方式、复现步骤及预期结果。日志和截图先移除账号、地址、凭据和业务内容。

Pull Request 说明具体问题、改动后的行为、实际执行的验证及未覆盖的环境。修改部署、命令或用户流程时同步相关文档；保持改动集中，避免混入格式化或编译产物。

提交的贡献按项目 [MIT 许可证](LICENSE) 提供。保留引用代码和依赖的原始许可、署名与必要声明。
