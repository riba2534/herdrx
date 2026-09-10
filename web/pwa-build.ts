import { createHash } from 'node:crypto'
import { readdir, readFile, writeFile } from 'node:fs/promises'
import { join, resolve } from 'node:path'
import { brotliCompressSync, constants as zlibConstants, gzipSync } from 'node:zlib'
import type { Plugin } from 'vite'
import { selectPrecache } from './src/lib/precache'

const bootSrc = '/boot.js'

async function compressAssets(output: string) {
  const assetDir = join(output, 'assets')
  let names: string[] = []
  try {
    names = await readdir(assetDir)
  } catch {
    return
  }
  await Promise.all(names.filter((name) => /\.(js|css)$/.test(name)).map(async (name) => {
    const buf = await readFile(join(assetDir, name))
    await writeFile(join(assetDir, `${name}.gz`), gzipSync(buf, { level: 9 }))
    await writeFile(join(assetDir, `${name}.br`), brotliCompressSync(buf, {
      params: { [zlibConstants.BROTLI_PARAM_QUALITY]: 11 },
    }))
  }))
}

export function pwaBuild(): Plugin {
  let output = ''
  let root = ''
  return {
    name: 'herdrx-pwa-build',
    apply: 'build',
    configResolved(config) {
      root = config.root
      output = resolve(config.root, config.build.outDir)
    },
    async transformIndexHtml(html: string) {
      const boot = await readFile(join(root, 'public/boot.js'), 'utf8')
      if (!html.includes(`<script src="${bootSrc}"></script>`)) {
        throw new Error('index.html must load /boot.js so the build can inline it')
      }
      return html.replace(`<script src="${bootSrc}"></script>`, `<script>${boot}</script>`)
    },
    async closeBundle() {
      await compressAssets(output)
      const files = (await readdir(output, { recursive: true, withFileTypes: true }))
        .filter((entry) => entry.isFile() && entry.name !== 'sw.js')
        .map((entry) => join(entry.parentPath, entry.name).slice(output.length).replaceAll('\\', '/')).sort()
      const html = await readFile(join(output, 'index.html'), 'utf8')
      const manifest = JSON.parse(await readFile(join(output, 'manifest.webmanifest'), 'utf8')) as { icons?: { src?: string }[] }
      const precache = selectPrecache(files, html, manifest)
      if (!precache.includes('/index.html') || !precache.some((file) => file.startsWith('/assets/'))) {
        throw new Error('PWA build is missing its app shell')
      }
      const digest = createHash('sha256')
      for (const file of precache) digest.update(file).update(await readFile(join(output, file)))
      const template = await readFile(join(output, 'sw.js'), 'utf8')
      digest.update(template)
      await writeFile(join(output, 'sw.js'), template.replace('__BUILD_ID__', digest.digest('hex').slice(0, 24)).replace('/* __PRECACHE__ */ []', JSON.stringify(precache)))
    },
  }
}
