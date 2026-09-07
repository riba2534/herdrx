# herdrx v0.1.0 发行概览

首个公开版本采用 `v0.1.0-rc.1` 预发布标签，功能、下载与限制见 [RC 发布说明](releases/v0.1.0-rc.1.md)。正式 `v0.1.0` 需要完成[正式版验收清单](release-readiness-2026-09-06.md)，不会用 RC 的短时测试替代长期和真实设备记录。

## 交付内容

- 网站：多用户账号、邀请注册、访问管理、SSH/Tailcat/本机接入、主机与密钥管理、终端工作区和移动界面。
- CLI：Linux amd64/arm64 静态程序、systemd 用户服务、一次性绑定、网络诊断、自建中继、签名更新与回滚。
- 部署：默认 HTTP 的 nonroot 双架构网站镜像、可见 bind mount 数据目录、健康检查、备份恢复与升级说明。
- 发行材料：源码标签、GitHub Release 附件、SHA256SUMS、Ed25519 清单、MIT LICENSE、第三方依赖声明和 CycloneDX SBOM。

工作台只提供访问。Herdr 与任务独立运行，关闭网页、退出账号、更新网站或重启访问 CLI 均不应结束远程 pane 或任务；远程主机自身重启后的任务恢复由 Herdr 和任务本身决定。

## 维护与验证

[本地验收记录](release-validation-2026-09-07.md) 区分实际协议、原生架构、浏览器模拟与实体设备。主分支验证和 Docker Hub 分发由 [CI](https://github.com/riba2534/herdrx/actions/workflows/ci.yml) 执行；版本标签触发 [CLI 发布](https://github.com/riba2534/herdrx/actions/workflows/cli-release.yml)，同一主分支提交的完整 CI 未通过时拒绝公开附件。

安装与运维见 [安装说明](install.md)、[Tailcat 教程](tailcat-quickstart.md)、[部署](deployment.md)、[运维](operations.md) 和[更新与恢复](update-and-recovery.md)。
