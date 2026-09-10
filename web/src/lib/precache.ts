const bootSrc = '/boot.js'

export function referencedIcons(html: string, manifest: { icons?: { src?: string }[] }) {
  const found = new Set<string>()
  for (const match of html.matchAll(/\b(?:href|src)="(\/[^"]+)"/g)) {
    const href = match[1]
    if (href === '/favicon.ico' || href.startsWith('/brand/')) found.add(href)
  }
  for (const icon of manifest.icons || []) {
    if (icon.src?.startsWith('/')) found.add(icon.src)
  }
  return [...found].sort()
}

export function selectPrecache(files: string[], html: string, manifest: { icons?: { src?: string }[] }) {
  const allow = new Set<string>(['/index.html', '/manifest.webmanifest'])
  if (html.includes(`src="${bootSrc}"`) && files.includes(bootSrc)) allow.add(bootSrc)
  for (const file of files) {
    if (file.startsWith('/assets/') && !file.endsWith('.gz') && !file.endsWith('.br')) allow.add(file)
  }
  for (const icon of referencedIcons(html, manifest)) {
    if (files.includes(icon)) allow.add(icon)
  }
  return files.filter((file) => allow.has(file)).sort()
}
