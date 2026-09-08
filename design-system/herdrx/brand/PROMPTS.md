# 高清品牌素材的最终生成提示词

工具：内置 `image_gen`。以下为最终采用的三个结果的提示词；参考图片为对应旧稿或前一步生成结果。

## 图标

参考：原有 `icon-source.png`。

```text
Use case: precise-object-edit. Asset type: production application icon for herdrx, high-resolution master (prefer 2048x2048 or higher square). Input image 1 is the existing authoritative logo icon, edit this exact artwork. Primary request: make a faithful, exceptionally crisp high definition version of the same silver-white and muted-gold interlocking H ribbon with its right-pointing negative-space arrow, on the same deep blue rounded square tile. Preserve the existing silhouette, proportions, interlocking order, orientation, negative space, and recognizable identity; do not redesign the brand. Correct the source's soft gradients and fuzzy edges by rendering with precisely defined clean geometry and flat solid fills. Use tile #193747, silver #e2ecf0, gold #dbc37e. The tile occupies the square canvas with ~2% transparent outer margin, its radius consistent with the original. Give every shape a solid opaque interior and crisp antialiased contours. Absolutely no soft shadow, halo, glow, blur, texture, lighting, gradient, bevel or 3D. No added text, labels, alternative options, presentation sheet, surrounding frame, or mockup. Output a single finished front-facing icon with genuine alpha transparency only outside the rounded tile; no checkerboard drawn into pixels. This is intended for small 16/24/32px icons as well as high resolution desktop/PWA icons: preserve bold structure and sharply distinguish the three solid colors.
```

实际输出为 1254×1254；记录实际尺寸，不将生成请求中的期望尺寸作为结果。

## 字标亮度遮罩

参考：保留原 herdrx 字形的独立字标试稿。

```text
Create a production typography mask from this exact herdrx wordmark. Preserve the six lowercase glyph silhouettes and their spacing. Render the lettering pure solid WHITE #ffffff on a pure solid BLACK #000000 background. Every letter interior is flat pure white; all background and all letter holes are pure black. Very sharp, clean contours with only a thin antialiased edge. NO gray checkerboard, NO transparency, NO shadows, NO glow, NO gradients, NO texture, NO icon, NO extra wording. Just the large exact word herdrx in white on black, centered on a wide canvas, nearly filling its width with 5% blank margins. This is a clean high-resolution monochrome luminance mask, so black and white regions must be perfectly uniform.
```

## 横版 Logo

参考：本目录的最终图标和字标遮罩。

```text
Create the final high-resolution horizontal herdrx logo, at least 2048px wide. Use image 1 as the exact silver and muted-gold interlocking H/forward-arrow icon. Use image 2 as the exact lowercase herdrx wordmark shape and spacing. Place the icon at left and the wordmark to its right on one optical baseline, icon about 27% of the overall visible width and a clear small gap to the text. Fill the entire canvas with uniform opaque deep blue #193747. The icon's deep blue tile blends into this same background. Render the wordmark in solid silver-white #e2ecf0 with crisp clean edges. Preserve both reference identities faithfully. No transparency is requested: this is an opaque dark-background horizontal brand asset. NO checkerboard, NO glow, NO shadow, NO gradients, NO blur, NO surrounding illustrations, NO mockup, NO tagline, NO extra text, NO alternatives. A single finished sharp production logo with small uniform outer margins.
```
