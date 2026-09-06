# herdrx 品牌素材

新版标识由两条交错的银白与淡金色带组成 H，中间形成向前的箭头，表达多主机工作台中的连接与继续。图形和小写 herdrx 字标使用内置 image_gen 生图工具生成。

- [图标原稿](icon-source.png) 和 [横向 Logo 原稿](logo-source.png) 保留生成的 PNG 与透明度。
- [网站 Logo](../../../web/public/brand/logo.png) 可作为横向素材使用。
- [512 px 图标](../../../web/public/brand/icon-512.png) 与 16/32/48/64/128/192 px 导出共享同一原稿。
- [手机桌面图标](../../../web/public/brand/apple-touch-icon.png) 为 180 px，提供不透明底色。
- [可裁切应用图标](../../../web/public/brand/icon-maskable-512.png) 为 512 px，留出安全区域。
- 网页导航复用生成图形和字标，字标透明度作为 CSS mask，由界面文字颜色填充，浅色和深色主题使用完全相同的字形。Logo 保持 96×24 px，不增加顶部高度。

## 导出

先安装项目的前端依赖与 Chromium，然后运行：

```bash
node scripts/export-brand-assets.mjs
make web-build
```

脚本只进行透明留白裁切、等比缩放、PNG/ICO 编码及应用图标背景/安全留白处理，不重绘图形。素材由网站自身提供，无外部图床或运行时生图依赖。浏览器图标通过 HTML 的 icon/apple-touch-icon 与 manifest 引用。

## 图标生成提示词

```text
Use case: logo-brand.
Asset type: production application icon and brand symbol for "herdrx", a compact professional web workbench that connects multiple remote computers and terminal panes.
Primary request: design one original, polished, exceptionally simple geometric monogram icon. Two interlocking terminal-pane/ribbon forms suggest the letter h or H and a small forward-pointing negative-space notch. The mark should convey connected workspaces, precision, and continuity. A strong unified silhouette that remains recognizable at 16 and 24 pixels. No tiny interior details.
Style: flat vector-like brand design with extremely clean edges, confident proportions, restrained corner rounding. Mature developer software identity.
Composition: one large icon centered in a square canvas. A rounded-square deep blue-gray tile fills the canvas with only about 4% transparent outside margin. Inside it, the bold interlocking monogram is optically centered and fills roughly 65% of the tile width and height, with generous uniform clearspace.
Color palette: tile #193747, primary monogram #e2ecf0, one restrained complementary segment #dbc37e. Flat solid colors only. This must work on both light and dark websites.
Text: no words, no letters typeset as text, no captions, no wordmark.
Constraints: output ONLY the finished single icon asset on genuinely transparent background outside the rounded tile, alpha transparency preserved. Front-facing flat artwork. No mockup, no presentation board, no multiple options, no surrounding labels, no watermark.
Avoid: gradients, glows, lighting, shadows, bevels, 3D, fine strokes, mascot, generic terminal window with >_, circuit-board decoration, extra ornaments.
```

## 横向 Logo 生成提示词

输入参考为本目录的 icon-source.png。

```text
Use case: logo-brand.
Asset type: finished horizontal website logo for herdrx, with a genuine transparent background.
Input image 1: authoritative reference for the exact application icon and brand identity; preserve this interlocking silver and muted-gold H/forward arrow symbol and its deep-blue rounded tile.
Primary request: create the matching horizontal brand lockup. Place a faithful, crisp rendition of the supplied icon at the left, and the exact lowercase word "herdrx" to its right. Letter-by-letter: h e r d r x. Use a custom-looking, restrained, sturdy geometric sans serif wordmark, clean subtly rounded forms, medium-semibold weight, excellent kerning, compact proportions appropriate for a terminal workbench.
Color palette: wordmark solid deep blue-gray #193747; icon retains the input colors.
Composition: very wide horizontal canvas approximately 4.5:1; icon and wordmark together occupy almost all of the canvas with small transparent clearspace around their actual bounds. Icon slightly taller than the wordmark letter height; gap roughly one third of the icon width. Everything centered on one horizontal optical baseline.
Constraints: only ONE production logo, no additional rows or versions, no labels, no tagline, no mockup, no presentation background. Use actual transparent alpha outside the logo, no white or checkerboard pixels. Preserve the exact icon silhouette and color order from the reference. Text must read herdrx exactly in lowercase. No glow, shadows, gradients, texture, 3D or extra decorative elements.
```
