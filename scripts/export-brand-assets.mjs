#!/usr/bin/env node
// Export the generated artwork at web/app sizes without redrawing the identity.
import { readFile, writeFile, mkdir } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'
import { chromium } from '../web/node_modules/@playwright/test/index.mjs'

const root = fileURLToPath(new URL('../', import.meta.url))
const output = join(root, 'web/public/brand')
await mkdir(output, { recursive: true })
const sources = Object.fromEntries(await Promise.all(['icon', 'logo'].map(async (name) => [name, 'data:image/png;base64,' + (await readFile(join(root, `design-system/herdrx/brand/${name}-source.png`))).toString('base64')])))
const browser = await chromium.launch()
try {
  const page = await browser.newPage()
  const assets = await page.evaluate(async (sources) => {
    const load = async (url) => {
      const image = new Image()
      image.src = url
      await image.decode()
      return image
    }
    const icon = await load(sources.icon)
    const logo = await load(sources.logo)
    const results = {}
    const bounds = (image, fromX = 0) => {
      const canvas = document.createElement('canvas')
      canvas.width = image.width; canvas.height = image.height
      const ctx = canvas.getContext('2d')
      ctx.drawImage(image, 0, 0)
      const { data } = ctx.getImageData(0, 0, canvas.width, canvas.height)
      let left = image.width, top = image.height, right = 0, bottom = 0
      for (let y = 0; y < image.height; y++) for (let x = fromX; x < image.width; x++) {
        if (data[(y * image.width + x) * 4 + 3] < 128) continue
        left = Math.min(left, x); top = Math.min(top, y)
        right = Math.max(right, x + 1); bottom = Math.max(bottom, y + 1)
      }
      if (right <= left || bottom <= top) throw new Error('Generated artwork has no visible content')
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
    for (const size of [16, 32, 48, 64, 128, 192, 512]) exportPNG(`icon-${size}.png`, icon, size, size)
    exportPNG('apple-touch-icon.png', icon, 180, 180, undefined, 0, '#193747')
    // Opaque square background and 12.5% inset keep the symbol in the maskable safe area.
    exportPNG('icon-maskable-512.png', icon, 512, 512, undefined, 64, '#193747')
    const logoBounds = bounds(logo)
    exportPNG('logo.png', logo, 768, Math.round(768 * logoBounds[3] / logoBounds[2]), logoBounds)
    // The generated wordmark is separate so CSS can match its ink to either UI theme.
    const wordmarkBounds = bounds(logo, Math.floor(logo.width * 0.31))
    exportPNG('wordmark.png', logo, 256, Math.round(256 * wordmarkBounds[3] / wordmarkBounds[2]), wordmarkBounds)
    return results
  }, sources)
  for (const [name, data] of Object.entries(assets)) await writeFile(join(output, name), Buffer.from(data, 'base64'))
  const sizes = [16, 32, 48]
  const header = Buffer.alloc(6 + sizes.length * 16)
  header.writeUInt16LE(1, 2); header.writeUInt16LE(sizes.length, 4)
  const frames = sizes.map((size) => Buffer.from(assets[`icon-${size}.png`], 'base64'))
  let offset = header.length
  for (let i = 0; i < sizes.length; i++) {
    const entry = 6 + i * 16
    header[entry] = sizes[i]; header[entry + 1] = sizes[i]
    header.writeUInt16LE(1, entry + 4); header.writeUInt16LE(32, entry + 6)
    header.writeUInt32LE(frames[i].length, entry + 8); header.writeUInt32LE(offset, entry + 12)
    offset += frames[i].length
  }
  await writeFile(join(root, 'web/public/favicon.ico'), Buffer.concat([header, ...frames]))
  console.log(`Exported ${Object.keys(assets).length} PNG assets and a 16/32/48 px favicon from generated artwork`)
} finally { await browser.close() }
