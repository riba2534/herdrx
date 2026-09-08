# herdrx 高清品牌素材

保留银白与淡金色交错 H、向前箭头及深蓝配色。原稿通过内置 `image_gen` 生成，网页使用独立图标和字标，不再将旧横版 Logo 中的半透明光晕作为字形显示。

| 原稿 | 尺寸 | 用途 |
|---|---:|---|
| [icon-source.png](icon-source.png) | 1254×1254 | 透明圆角图标，网页与应用图标共用 |
| [wordmark-source.png](wordmark-source.png) | 2048×768 | 黑底白字的独立亮度遮罩，保留 herdrx 字形 |
| [logo-source.png](logo-source.png) | 2172×724 | 深蓝底横版 Logo，用于独立展示 |

生成使用内置工具，无 CLI/API 回退。最终提示词见 [PROMPTS.md](PROMPTS.md)。棋盘格背景的试稿未进入项目。

## 网页与应用资源

正式资源位于 [web/public/brand/v2](../../../web/public/brand/v2/)，更换路径使浏览器与 PWA 能请求新版图标。根目录 `favicon.ico` 同时更新，兼容浏览器的默认请求。

- 网页图标提供 1×、2×、3×资源，覆盖实际 18、24、28、52 CSS px 的使用位置。
- 字标采用 `mask-mode: luminance`：黑色隐藏、白色显示，再由主题文字颜色填充。黑白原稿不能当作普通透明图片直接显示，也不能改成默认 alpha mask。
- 导出 16 至 1024 px 的 PNG、180 px Apple Touch 图标，以及保留不透明背景和 12.5% 留白的 512 px maskable 图标。
- 横版 Logo 为实色底高清图片；网页导航使用图标加亮度遮罩，保持 96×24 px 的原有布局。

## 导出

```sh
node scripts/export-brand-assets.mjs
make web-build
```

导出脚本裁切外部留白、等比缩放并编码 PNG/ICO，不重绘字形。字标以亮度 8/255 定位裁剪边界并外扩 4px，避免近黑噪点扩大画布；裁剪框内保留原始 RGB/alpha，不做阈值抠图。亮度遮罩在浏览器中按其实际亮度渲染。
