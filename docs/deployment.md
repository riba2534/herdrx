# 构建与镜像发布

部署链路固定为：GitHub `main` 推送 → Actions 验证和构建 → ZOT 私有镜像源 → 目标机器手动拉取、启动。网站与受控端 CLI 是两类产物，此流水线只发布网站 Docker 镜像。

## 镜像位置与标签

镜像引用示例：`registry.example.com/herdrx/server`。此域名是占位符，真实地址由维护者在私有配置中提供。

| 引用 | 用途 |
| --- | --- |
| `:latest` | 最近通过验证并发布的当前 main 构建，供日常拉取 |
| `:sha-<完整提交 SHA>` | 同一提交的 linux/amd64 + linux/arm64 清单 |
| `:sha-<完整提交 SHA>-amd64` / `-arm64` | 各架构镜像 |
| `@sha256:<digest>` | 精确固定已验证产物，适合回退和可复现部署 |

重新运行旧提交可以保留其提交镜像，但不会将 `latest` 倒退。提交标签可能因重跑构建而改变，要求字节一致时使用 digest。首次发布成功前，仓库中没有可供部署的 `latest`。

## Actions 做什么

配置为 [ci.yml](../.github/workflows/ci.yml)，仅给 workflow `contents: read` 权限。

1. 安装锁定工具链和前端依赖，执行 typecheck、lint、Vitest、前端构建。
2. 执行 Go 模块校验、vet、普通全量测试与 race 测试；检查 Compose 无命名卷、所有挂载都是 bind。
3. 在 GitHub 原生 amd64、arm64 runner 各自构建镜像，启动 nonroot 容器，检查健康与静态资源、创建管理员、重建容器并验证登录数据保留。
4. PR 到这里结束；只有本仓库 main 的推送/手动运行可进入 `zot` Environment。
5. 导出已测镜像及校验和。发布 job 加载两份候选，核对架构和提交，不重新构建。
6. 使用专用 ZOT 凭据推送，按 digest 拉回并核对镜像 ID，组成双架构清单；确认当前 main SHA 后更新 latest。
7. 上传 `herdrx-deploy-*` 附件，内含部署压缩包、`release.env` 镜像模板（精确 digest + 域名占位符）和 `verification.txt` 发布记录。

任一门禁失败均不更新 latest。推送中途故障可能留下架构标签，但目标机的 latest 保持原状。CI 不登录目标服务器，也不会替你重建正在运行的实例。

## GitHub 环境配置

在仓库 Settings → Environments 建立 `zot`，部署分支仅允许 `main`。不要给 PR 注入发布凭据，也不要使用 `pull_request_target` 执行提交者代码。

| 类型 | 名称 | 值 |
| --- | --- | --- |
| Secret | `ZOT_REGISTRY` | 自己的镜像源主机名，可带端口，不带协议或路径 |
| Variable | `ZOT_REPOSITORY` | `herdrx/server` |
| Secret | `ZOT_USERNAME` | 专用发布账号 `herdrx-ci` |
| Secret | `ZOT_PASSWORD` | 对应随机口令 |

`ZOT_REGISTRY` 必须使用 Secret，使 Actions 日志自动遮蔽实际地址；不要改用公开配置或在脚本中打印编码后的地址。Actions 附件不会经过日志脱敏，因此只包含仓库内路径、标签、digest 和通用域名占位符。

Fork 后若要自动发布自己的镜像，需要修改 publish job 的仓库白名单、配置自己的环境变量和凭据，并修改部署 `.env` 的 `HERDRX_IMAGE`。

## ZOT 权限

ZOT 通过有效 HTTPS 对 GitHub runner 和目标机器开放；无需把镜像源所在机器的 SSH 凭据交给 GitHub。

- `herdrx-ci`：仅 `herdrx/server` 的 `read`、`create`、`update`；`update` 用于移动 latest 标签。
- `herdrx-pull`：仅 `herdrx/server` 的 `read`。
- 不赋予这两个账号全仓库权限或删除权限，保留其他项目原有策略。

凭据只保存在 GitHub Environment 或仓库外权限为 `0600` 的本机配置文件。目标机器通过 `docker login ... --username herdrx-pull` 输入密码，不把密码写到 Compose 或 Git。

## 部署与验收

目标机解压 Actions 附件中的部署包，复制 `.env.example` 为 `.env`，填写自己的镜像引用和 URL，执行 `sudo bash prepare-data.sh`，登录 ZOT，然后：

```bash
docker compose pull
docker compose up -d --wait
docker compose ps
docker compose exec herdrx /app/herdrx-server healthcheck
```

需要精确部署本次产物时，先将 `release.env` 中的 `registry.example.com` 替换为自己私下配置的镜像源，再把 `HERDRX_IMAGE` 行复制进自己的 `.env`，保留原有网站域名和其他配置。

目标机只需要 Docker 与部署文件。不要把源码 `data/` 或开发机身份混入镜像，也不要让两个实例同时使用同一数据目录。备份与 HTTPS 见 [运维](operations.md)，升级回退见 [更新与恢复](update-and-recovery.md)。

## 本地验证

```bash
python3 scripts/check-deployment.py
python3 scripts/test-publish-images.py
docker buildx build --platform linux/amd64 --provenance=false --build-arg VERSION=local -f deploy/Dockerfile -t herdrx-server:local --load .
python3 scripts/smoke-image.py herdrx-server:local local
```

镜像冒烟需要 Docker 和免交互 sudo，在隔离临时目录运行并清理。arm64 必须在对应原生 runner 实际启动后才能视为该架构验收通过，交叉编译成功本身不等于运行通过。

## 本次访问管理变更的部署要求

发布附件只包含 HTTP 网站部署配置，不附带 TLS 代理或证书配置。部署者可自行添加反向代理；外部 URL、Secure Cookie 和可信代理参数见 [运维](operations.md)。直连模式不设置可信代理。

可执行 `python3 scripts/test-caddy-auth.py <候选镜像>` 验证真实 Caddy HTTPS、Secure Cookie、转发来源防伪和不同客户端的登录限流；此项也进入双架构镜像验收。

CI 另行执行真实 Chromium 登录、邀请、跨标签页认证、用户禁用、审计和窄屏检查。两种架构镜像仍分别在原生 runner 完成容器持久化冒烟后才允许发布。独立远程主机的整机故障验收与日常 CI 分开记录，不能用单机容器重启替代。
