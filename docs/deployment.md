# Docker Hub 镜像发布（维护者）

使用者部署见 [README](../README.md#快速开始)。网站镜像由 GitHub `main` 推送触发 Actions，验证通过后自动发布到公开的 [riba2534/herdrx](https://hub.docker.com/r/riba2534/herdrx)。此流程发布网站 Docker 镜像；远程 CLI 使用独立的 GitHub Release 流程。

## 镜像标签

| 引用 | 用途 |
|---|---|
| `riba2534/herdrx:latest` | 最新通过验证的当前 main 构建 |
| `riba2534/herdrx:sha-<完整提交 SHA>` | 对应提交的 Linux amd64 / arm64 清单 |
| `riba2534/herdrx:sha-<完整提交 SHA>-amd64` / `-arm64` | 对应架构的镜像 |
| `riba2534/herdrx@sha256:<digest>` | 精确固定产物，适合回退 |

旧提交手动重跑不会将 `latest` 倒退。提交标签可能因重跑构建改变，需要字节一致时使用 digest。拉取公开镜像不需要 Docker Hub 登录。

## Actions 流程

配置见 [ci.yml](../.github/workflows/ci.yml)，workflow 权限为 `contents: read`。

1. 安装锁定的工具链与前端依赖，运行 typecheck、lint、Vitest 和前端构建。
2. 运行 Go 模块校验、vet、全量与 race 测试、依赖声明和漏洞检查，以及浏览器认证、管理、终端、手机显示与 PWA 回归。
3. 检查部署配置、镜像发布失败路径与 CLI 安装器。
4. 在 GitHub 原生 amd64、arm64 runner 各自构建并实际启动镜像，验证 nonroot、目录挂载、管理员初始化、重建保留数据及 HTTPS 代理认证。
5. PR 到此结束。仅本仓库 main 推送或手动运行可进入 `dockerhub` Environment。发布 job 加载同一次运行导出的已验证镜像和校验和，不重新构建。
6. 推送到 Docker Hub，按 digest 拉回核对镜像 ID，组成双架构清单；核对当前 main SHA 后更新 `latest`。
7. 上传 `herdrx-deploy-*` 附件，包含可选 Compose 部署包、可直接使用的 `release.env` digest 引用及 `verification.txt` 发布记录。

任一验证失败均不发布 `latest`。推送中途故障可能留下架构标签，但不会进入 `latest` 更新步骤。CI 不登录用户工作台主机部署，不更新或重启远程 Herdr。

## GitHub 环境配置

仓库 Settings → Environments 使用 `dockerhub`，部署分支仅允许 `main`。

| 类型 | 名称 | 值 |
|---|---|---|
| Variable | `DOCKERHUB_USERNAME` | `riba2534` |
| Variable | `DOCKERHUB_REPOSITORY` | `riba2534/herdrx` |
| Secret | `DOCKERHUB_TOKEN` | 具备目标仓库读写权限的 Docker Hub PAT |

Token 仅通过 Environment Secret 注入 Docker 登录动作；不写入源码、环境示例、发布附件或日志。Docker 登录使用独立临时配置目录。PR 不注入发布凭据，不使用 `pull_request_target` 执行提交者代码。

Fork 后需要修改 publish job 的仓库白名单，并配置自己的公开 Docker Hub 仓库、用户名和 Token；同步使用说明与可选 Compose 的镜像引用。

## 本地验证

```bash
python3 scripts/check-deployment.py
python3 scripts/test-publish-images.py
docker buildx build --platform linux/amd64 --provenance=false --build-arg VERSION=local -f deploy/Dockerfile -t herdrx-server:local --load .
python3 scripts/smoke-image.py herdrx-server:local local
```

镜像冒烟使用隔离临时目录，需要 Docker 和无交互 sudo。arm64 必须在原生 runner 实际启动验证；交叉编译不能代替运行验收。发布流程变更需执行项目完整门禁。

部署默认 HTTP，本地 `./data` 目录持久化；可选 Compose 也仅拉取公开镜像。备份与自配 HTTPS 见 [运维](operations.md)，升级回退见 [更新与恢复](update-and-recovery.md)。
