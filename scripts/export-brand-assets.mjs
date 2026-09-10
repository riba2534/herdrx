#!/usr/bin/env node
// Export approved high-resolution artwork without redrawing or removing backgrounds.
import { readFile, writeFile, mkdir } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { join, resolve } from 'node:path'
import { parseArgs } from 'node:util'
import { chromium } from '../web/node_modules/@playwright/test/index.mjs'

const root = fileURLToPath(new URL('../', import.meta.url))
const publicBrand = join(root, 'web/public/brand/v2')
const archivedBrand = join(root, 'design-system/herdrx/brand/exported')
const publicNames = new Set([
  'apple-touch-icon.png', 'icon-32.png', 'icon-64.png', 'icon-128.png', 'icon-192.png', 'icon-512.png',
  'icon-maskable-192.png', 'icon-maskable-512.png', 'wordmark.png',
])
const { values } = parseArgs({ options: { 'wordmark-source': { type: 'string', default: 'design-system/herdrx/brand/wordmark-source.png' } } })
const sourcePaths = {
  icon: join(root, 'design-system/herdrx/brand/icon-source.png'),
  logo: join(root, 'design-system/herdrx/brand/logo-source.png'),
  // A separately approved black-background / white-letter luminance mask.
  // Do not infer text from a percentage crop of the full logo or remove its glow.
  wordmark: resolve(root, values['wordmark-source']),
}
const sources = Object.fromEntries(await Promise.all(Object.entries(sourcePaths).map(async ([name, path]) => [name, 'data:image/png;base64,' + (await readFile(path)).toString('base64')])))
const browser = await chromium.launch()
try {
  const page = await browser.newPage()
  const { assets, dimensions } = await page.evaluate(async (sources) => {
    const load = async (url) => {
      const image = new Image()
      image.src = url
      await image.decode()
      return image
    }
    const icon = await load(sources.icon)
    const logo = await load(sources.logo)
    const wordmark = await load(sources.wordmark)
    if (icon.width !== icon.height || icon.width < 1024) throw new Error('Icon source must be square and at least 1024 × 1024 pixels')
    const results = {}
    const wordmarkBounds = (image) => {
      const canvas = document.createElement('canvas')
      canvas.width = image.width; canvas.height = image.height
      const ctx = canvas.getContext('2d')
      ctx.drawImage(image, 0, 0)
      const { data } = ctx.getImageData(0, 0, canvas.width, canvas.height)
      let left = image.width, top = image.height, right = 0, bottom = 0
      for (let y = 0; y < image.height; y++) for (let x = 0; x < image.width; x++) {
        const offset = (y * image.width + x) * 4
        // Ignore near-black background noise only while locating the crop.
        // The cropped artwork keeps its original RGB/alpha and antialiased edges.
        const luminance = (0.2126 * data[offset] + 0.7152 * data[offset + 1] + 0.0722 * data[offset + 2]) * data[offset + 3] / 255
        if (luminance < 8) continue
        left = Math.min(left, x); top = Math.min(top, y)
        right = Math.max(right, x + 1); bottom = Math.max(bottom, y + 1)
      }
      if (right <= left || bottom <= top) throw new Error('Wordmark source has no visible luminance')
      left = Math.max(0, left - 4); top = Math.max(0, top - 4)
      right = Math.min(image.width, right + 4); bottom = Math.min(image.height, bottom + 4)
      return [left, top, right - left, bottom - top]
    }
    const exportPNG = (name, image, width, height, crop = [0, 0, image.width, image.height], inset = 0, background) => {
      const canvas = document.createElement('canvas')
      canvas.width = width; canvas.height = height
      const ctx = canvas.getContext('2d')
      if (background) { ctx.fillStyle = background; ctx.fillRect(0, 0, width, height) }
      ctx.imageSmoothingEnabled = true; ctx.imageSmoothingQuality = 'high'
      ctx.drawImage(image, ...crop, inset, inset, width - inset * 2, height - inset * 2)
      results[name] = canvas.toDataURL('image/png').split(',')[1]
    }
    for (const size of [16, 32, 48, 64, 128, 192, 256, 512, 1024]) exportPNG(`icon-${size}.png`, icon, size, size)
    exportPNG('apple-touch-icon.png', icon, 180, 180, undefined, 0, '#193747')
    // Preserve the established tile size. Review the core H/arrow against the
    // central 40%-radius safe circle; the decorative tile may extend beyond it.
    // https://www.w3.org/TR/appmanifest/#icon-masks
    exportPNG('icon-maskable-512.png', icon, 512, 512, undefined, 64, '#193747')
    exportPNG('icon-maskable-192.png', icon, 192, 192, undefined, 24, '#193747')
    // The full logo may have an intentional solid background. Preserve it and
    // its native resolution instead of treating the background as text content.
    exportPNG('logo.png', logo, logo.width, logo.height)
    const crop = wordmarkBounds(wordmark)
    if (crop[2] < 192) throw new Error('Wordmark artwork must be at least 192 pixels wide for its 64px display at 3× density')
    const wordmarkWidth = Math.min(1024, crop[2])
    exportPNG('wordmark.png', wordmark, wordmarkWidth, Math.round(wordmarkWidth * crop[3] / crop[2]), crop)
    return { assets: results, dimensions: { icon: [icon.width, icon.height], logo: [logo.width, logo.height], wordmarkSource: [wordmark.width, wordmark.height], wordmarkCrop: crop } }
  }, sources)
  await mkdir(publicBrand, { recursive: true })
  await mkdir(archivedBrand, { recursive: true })
  for (const [name, data] of Object.entries(assets)) {
    await writeFile(join(publicNames.has(name) ? publicBrand : archivedBrand, name), Buffer.from(data, 'base64'))
  }
  const sizes = [16, 32, 48, 64, 128, 256]
  const header = Buffer.alloc(6 + sizes.length * 16)
  header.writeUInt16LE(1, 2); header.writeUInt16LE(sizes.length, 4)
  const frames = sizes.map((size) => Buffer.from(assets[`icon-${size}.png`], 'base64'))
  let offset = header.length
  for (let i = 0; i < sizes.length; i++) {
    const entry = 6 + i * 16
    // ICO stores a 256px dimension as zero in its one-byte directory entry.
    header[entry] = sizes[i] === 256 ? 0 : sizes[i]; header[entry + 1] = header[entry]
    header.writeUInt16LE(1, entry + 4); header.writeUInt16LE(32, entry + 6)
    header.writeUInt32LE(frames[i].length, entry + 8); header.writeUInt32LE(offset, entry + 12)
    offset += frames[i].length
  }
  const favicon = Buffer.concat([header, ...frames])
  // Browsers request /favicon.ico directly; keep a single copy at the site root.
  await writeFile(join(root, 'web/public/favicon.ico'), favicon)
  console.log(JSON.stringify({
    public: 'web/public/brand/v2',
    archived: 'design-system/herdrx/brand/exported',
    pngAssets: Object.keys(assets).length,
    faviconSizes: sizes,
    sourceDimensions: dimensions,
  }, null, 2))
} finally { await browser.close() }
