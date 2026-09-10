import { describe, expect, it } from 'vitest'
import { referencedIcons, selectPrecache } from './precache'

const html = `<!doctype html>
<link rel="icon" href="/favicon.ico" />
<link rel="icon" href="/brand/v2/icon-192.png" />
<link rel="apple-touch-icon" href="/brand/v2/apple-touch-icon.png" />
<script>inline</script>`

const manifest = {
  icons: [
    { src: '/brand/v2/icon-192.png' },
    { src: '/brand/v2/icon-maskable-192.png' },
    { src: '/brand/v2/icon-512.png' },
    { src: '/brand/v2/icon-maskable-512.png' },
  ],
}

const files = [
  '/index.html',
  '/boot.js',
  '/manifest.webmanifest',
  '/favicon.ico',
  '/sw.js',
  '/assets/index.js',
  '/assets/index.js.gz',
  '/assets/index.js.br',
  '/assets/index.css',
  '/assets/WorkbenchPage.js',
  '/brand/v2/icon-64.png',
  '/brand/v2/icon-192.png',
  '/brand/v2/icon-512.png',
  '/brand/v2/icon-maskable-192.png',
  '/brand/v2/icon-maskable-512.png',
  '/brand/v2/apple-touch-icon.png',
  '/brand/v2/logo.png',
  '/brand/v2/icon-1024.png',
  '/brand/v2/wordmark.png',
]

describe('selectPrecache', () => {
  it('keeps the app shell, assets, and referenced icons only', () => {
    const precache = selectPrecache(files, html, manifest)
    expect(precache).toEqual([
      '/assets/WorkbenchPage.js',
      '/assets/index.css',
      '/assets/index.js',
      '/brand/v2/apple-touch-icon.png',
      '/brand/v2/icon-192.png',
      '/brand/v2/icon-512.png',
      '/brand/v2/icon-maskable-192.png',
      '/brand/v2/icon-maskable-512.png',
      '/favicon.ico',
      '/index.html',
      '/manifest.webmanifest',
    ])
  })

  it('includes boot.js only when index.html still loads it as a file', () => {
    const withSrc = selectPrecache(files, html.replace('<script>inline</script>', '<script src="/boot.js"></script>'), manifest)
    expect(withSrc).toContain('/boot.js')
    expect(selectPrecache(files, html, manifest)).not.toContain('/boot.js')
  })

  it('does not precache unused brand artwork or compressed variants', () => {
    const precache = selectPrecache(files, html, manifest)
    expect(precache).not.toContain('/brand/v2/logo.png')
    expect(precache).not.toContain('/brand/v2/icon-1024.png')
    expect(precache).not.toContain('/brand/v2/icon-64.png')
    expect(precache).not.toContain('/brand/v2/wordmark.png')
    expect(precache).not.toContain('/assets/index.js.gz')
    expect(precache).not.toContain('/assets/index.js.br')
  })

  it('collects icon hrefs from index.html and the manifest', () => {
    expect(referencedIcons(html, manifest)).toEqual([
      '/brand/v2/apple-touch-icon.png',
      '/brand/v2/icon-192.png',
      '/brand/v2/icon-512.png',
      '/brand/v2/icon-maskable-192.png',
      '/brand/v2/icon-maskable-512.png',
      '/favicon.ico',
    ])
  })
})
