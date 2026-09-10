# 未随网站分发的品牌导出

这里保存页面和 PWA manifest 不会请求的高分辨率导出，避免进入 `web/public` 和 Service Worker 预缓存。网站实际使用的图标仍在 `web/public/brand/v2/`，站点 ICO 只有 `web/public/favicon.ico`。

重新导出：

```sh
node scripts/export-brand-assets.mjs
```
