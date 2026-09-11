# CLI GitHub Release

网站镜像通过 Docker Hub 流水线交付；远程主机 `herdrx` CLI 通过 GitHub Releases 交付。面向用户的安装说明见 [Tailcat 接入教程](tailcat-quickstart.md)。

## 构建本地候选包

使用项目声明的 Go 版本，构建 Linux 与 macOS amd64/arm64 的静态 CLI。输出目录必须为空：

```sh
python3 scripts/test-cli-installer.py
python3 scripts/build-cli-release.py --version v0.1.0-rc.1 --signing-key /secure/herdrx-release/ed25519.pem --out artifacts/cli-v0.1.0-rc.1
python3 scripts/verify-cli-release.py artifacts/cli-v0.1.0-rc.1
```

打包脚本仅编译 `cmd/herdrx`，不需要前端构建。压缩包固定包含 `herdrx`、`VERSION`、离线 README、MIT LICENSE 和第三方依赖声明，归档时间固定；清单使用当前 UTC 时间，重复构建需传入相同的 `--created-at YYYY-MM-DDTHH:MM:SSZ`；附件还包含安装脚本、SHA256SUMS、README-CLI.md、release.json、四个平台组合（linux/darwin × amd64/arm64）的 `.manifest.json`、RELEASE-PUBLIC-KEY、LICENSE、第三方声明及 CycloneDX SBOM。清单的 `goos` 字段区分平台，`internal/updater` 按 `runtime.GOOS`/`GOARCH` 精确匹配后才接受更新。release.json 记录源码提交和工作区是否有未提交内容。

验证脚本先用源码固定公钥核验清单签名，再检查每个附件的校验和、压缩包成员、二进制格式（Linux 校验 ELF 魔数与机器类型，macOS 校验 64 位小端 Mach-O 魔数与 CPU 类型）和二进制摘要，并在当前 OS/CPU 上运行 version/help/offline status。没有任何附件匹配当前 runner 时脚本报错而不是静默跳过——「没跑」和「跑过且通过」不能同形。格式检查不能代替在对应架构的主机上实际运行：ARM64 与 Intel Mac 的附件都需要各自实机验证。

## 发布流程

将经过审查和完整门禁验证的源码提交到仓库后，在该提交上创建版本标签并推送。标签格式为 `vX.Y.Z` 或 `vX.Y.Z-rc.N`。首次版本建议先使用预发布标签，保留 [发版差距](release-readiness-2026-09-06.md) 中未验收事项。

`.github/workflows/cli-release.yml` 在版本标签推送后执行：

1. 验证 Go modules、vet、全量测试、race 和安装器故障场景。
2. 从 `cli-release` Environment Secret 读取发行私钥，校验其公钥与源码固定信任根一致，构建并签名两种架构的同一版本附件，保存 Actions artifact。
3. 在 Linux x86_64 和 ARM64 runner 上分别验证附件并实际运行对应 CLI。macOS 附件目前只做格式与签名校验，未在 macOS runner 上实机运行；纳入 CI 需要增加 `macos-14`（ARM64）与 `macos-13`（x86_64）两个 runner。
4. 检查版本标签与候选包记录的提交一致、源码干净、提交已进入 `main`，且同一提交的完整主分支 CI 已成功，再上传 GitHub Release 草稿。
5. 下载全部草稿附件，与本次构建逐字节摘要核对，通过后才公开 Release。失败会保留草稿，用户安装页不会把草稿视为可用版本。

流程使用 GitHub 内置令牌，只有发布 job 获得 `contents: write` 与查询 CI 所需的 `actions: read`，不依赖 Docker Hub 凭据。手动 `workflow_dispatch` 仅生成和验证候选包，不创建标签或公开 Release。

首次安装器发布需要真实源码标签；不能把未提交工作区编译出的二进制挂到仅有 README 的初始提交上。发布脚本会拒绝 `source_dirty: true` 的候选包。需要在干净检出中重建后发布，不能手改元数据绕过检查。

发布脚本 `scripts/publish-cli-release.py` 还会复核远端标签，拒绝覆盖已公开版本。重新运行可以补全仍为草稿的同版本附件；若草稿含额外附件会拒绝公开。预发布不更新 latest；旧正式版本的补发也不会让 latest 低于已有正式版本。

## 网站安装入口

已登录用户打开 Tailcat 安装引导时，网站查询固定公开仓库的 Release 元数据；不发送用户凭据。下载入口必须同时具备两种架构包、对应签名清单、安装脚本、SHA256SUMS、离线教程和公钥，资产已上传且非空，并有合法版本标签。优先选正式版本，没有正式版本时展示预发布并明确标注。

网页提供固定版本命令，避免安装脚本和压缩包分别来自不同发布。网络超时、GitHub 限流或尚未发布时显示操作建议；已有 CLI 可以继续后续步骤。后台查询最多 5 秒，成功缓存 5 分钟，失败或无发布缓存 1 分钟，并发页面共享请求。

## 验收边界

本流水线验证 CLI 归档、下载、原子安装和基本命令，不等于完成两种架构的 systemd 登录/登出/reboot 验收、真实跨网或长期运行验收。首次安装以 GitHub HTTPS 引导信任，后续 `herdrx update` 使用内置公钥；附件验签与本地更新回归不代表已经公开发布或完成真实分发入口下载。

## 签名身份管理

发行信任根固定在 [`internal/updater/release.pub`](../internal/updater/release.pub)，编译进 CLI。附件 RELEASE-PUBLIC-KEY 供人工核对，验证器以可信源码文件为准。Ed25519 私钥不进入源码、附件或日志；保存在仓库外、仅所有者可读的 PEM 文件中，打包脚本会拒绝权限过宽、符号链接或公钥不匹配的文件。

GitHub 维护者在 `cli-release` Environment 的 `HERDRX_RELEASE_SIGNING_KEY` Secret 中配置同一 PEM 私钥，限制该环境只允许受信任的发行标签或分支使用。工作流将私钥写入 runner 的受限临时文件，构建后删除；缺失私钥会失败，不生成替代身份。设置密钥后不要在日志中打印 Secret，也不要把临时密钥用于正式发行。本仓库的发行环境限制为 `main` 分支和 `v*` 标签；fork 的维护者需要单独配置自己的信任根与签名环境。

版本专属发行说明放在 `docs/releases/<version>.md`，发布脚本使用该文件正文并附上准确源码提交；面向 Release 页的链接使用公开绝对地址。

为私钥做受限离线备份。丢失或泄露时停止使用该签名通道，通过独立可信渠道公布新公钥与迁移步骤；当前客户端不接受下载响应携带的公钥轮换。签名清单有效 180 天，正式版本不可覆盖，重新签发需使用新版本号。
