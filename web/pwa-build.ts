import { createHash } from 'node:crypto'
import { readdir, readFile, writeFile } from 'node:fs/promises'
import { join, resolve } from 'node:path'
import type { Plugin } from 'vite'

export function pwaBuild(): Plugin {
  let output = ''
  return {
    name: 'herdrx-pwa-build',
    apply: 'build',
    configResolved(config) { output = resolve(config.root, config.build.outDir) },
    async closeBundle() {
      const files = (await readdir(output, { recursive: true, withFileTypes: true }))
        .filter((entry) => entry.isFile() && entry.name !== 'sw.js')
        .map((entry) => join(entry.parentPath, entry.name).slice(output.length).replaceAll('\\', '/')).sort()
      if (!files.includes('/index.html') || !files.some((file) => file.startsWith('/assets/'))) throw new Error('PWA build is missing its app shell')
      const digest = createHash('sha256')
      for (const file of files) digest.update(file).update(await readFile(join(output, file)))
      const template = await readFile(join(output, 'sw.js'), 'utf8')
      digest.update(template)
      await writeFile(join(output, 'sw.js'), template.replace('__BUILD_ID__', digest.digest('hex').slice(0, 24)).replace('/* __PRECACHE__ */ []', JSON.stringify(files)))
    },
  }
}
