# 依赖清点与许可记录

本轮升级了可达的 SSH 安全修复及相关依赖：`golang.org/x/crypto v0.56.0`、`chi v5.3.0`、`golang-jwt/jwt v5.2.2`。修复后 `govulncheck` 源码调用分析未发现可达漏洞或已导入包漏洞；模块层仍列出 `golang.org/x/crypto/openpgp` 的停止维护告警，项目两个程序均不导入该包。该记录需要随依赖变化重新核验，不表示以后不会出现新的漏洞。前端本轮 `pnpm audit` 未报告漏洞。

`sbom.cdx.json` 使用 CycloneDX 1.6，列出两个 Linux 架构中 CLI/网站实际导入的 Go 模块，以及本次 Linux amd64 前端安装依赖（含构建和测试工具）。它是源码依赖清单，不是精确到符号的二进制组成证明，也不覆盖运行环境的操作系统包。发行附件的版本、提交和摘要由 release.json 与 SHA256SUMS 记录。

许可与署名见根目录 `THIRD_PARTY_NOTICES.md`，保留包内许可及各目录的 NOTICE/COPYRIGHT 等声明；重复文本按内容归并。Go runtime 的许可单独纳入。`caniuse-lite` 的浏览器兼容性数据采用 CC-BY-4.0，用于前端构建，原始署名和完整许可已保留。Go 和其余前端包的清点包含 MIT、ISC、BSD、Apache 等声明；主项目采用 [MIT](../LICENSE)；第三方依赖保留各自许可，不被主项目许可替代。

清点中的人工复核边界：

- `modernc.org/mathutil v1.7.1` 附带完整三条款 BSD 许可，旧版识别器未识别其排版。已读取该版本 LICENSE 并保留原文，清单标注 BSD-3-Clause。
- `saxes 6.0.0` 的 npm 压缩包没有独立许可文件，已从同版本上游源码补齐 `licenses/saxes-6.0.0.txt`，其来源为 `https://raw.githubusercontent.com/lddubeau/saxes/v6.0.0/LICENSE`。
- `react-remove-scroll-bar 2.3.8`、`stackback 0.0.2` 和构建工具 `@napi-rs/lzma-linux-x64-gnu 1.5.1` 的 npm 包声明 MIT，但未附独立许可文件。保留真实 package.json 的名称、版本、许可、作者和仓库声明，未伪造作者版权原文；`stackback` 内嵌 V8 格式化代码另有 BSD-3-Clause 头部，已完整保留。此项是分发材料缺口记录，不能将元数据声称为不存在的上游许可文件。
- esbuild、Rollup 和 oxlint 的平台子包使用对应同版本父包附带的许可。构建工具的本地原生包未被复制到网站运行镜像。

重新生成与检查：

```sh
python3 scripts/build-dependency-notices.py
python3 scripts/build-dependency-notices.py --check
govulncheck ./...
pnpm --dir web audit
```

生成器固定 `go-licenses v1.6.0`。新的未知许可、缺失声明或依赖版本变化会要求重新复核；生成过程不会修改包管理器锁文件。
