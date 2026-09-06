// Replaced with a content digest and the complete public build during Vite build.
const CACHE = 'herdrx-shell-67b655c70d59decff9983351'
const PRECACHE = ["/assets/AdminPage-Bhd16uBh.js","/assets/KeysPage-Cd5icmvp.js","/assets/WorkbenchPage-wk0hkKLb.js","/assets/index-Drn0FpU5.js","/assets/index-DzZEmeB_.css","/boot.js","/brand/apple-touch-icon.png","/brand/icon-128.png","/brand/icon-16.png","/brand/icon-192.png","/brand/icon-32.png","/brand/icon-48.png","/brand/icon-512.png","/brand/icon-64.png","/brand/icon-maskable-512.png","/brand/logo.png","/brand/wordmark.png","/favicon.ico","/index.html","/manifest.webmanifest"]
const publicPaths = new Set(PRECACHE)

self.addEventListener('install', (event) => {
  // A partial download must never replace the last working offline version.
  event.waitUntil(caches.open(CACHE).then((cache) => cache.addAll(
    PRECACHE.map((path) => new Request(path, { cache: 'reload', credentials: 'omit' })),
  )).catch(async (error) => { await caches.delete(CACHE); throw error }))
})

self.addEventListener('activate', (event) => event.waitUntil((async () => {
  // Explicit updates can leave other tabs running an older bundle. Keep their
  // lazy chunks until all pages have closed and an update activates naturally.
  const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
  if (windows.length === 0) {
    const keys = await caches.keys()
    await Promise.all(keys.filter((key) => /^herdrx-(shell|assets)-/.test(key) && key !== CACHE).map((key) => caches.delete(key)))
  }
  await self.clients.claim()
})()))

self.addEventListener('message', (event) => {
  if (event.data?.type === 'ACTIVATE_UPDATE' && event.source?.url && new URL(event.source.url).origin === self.location.origin) {
    event.waitUntil(self.skipWaiting())
  }
})

self.addEventListener('fetch', (event) => {
  const request = event.request
  const url = new URL(request.url)
  if (request.method !== 'GET' || url.origin !== self.location.origin) return
  // Never store credentials, API responses, terminal output, or queued input.
  if (url.pathname === '/api' || url.pathname.startsWith('/api/') || url.pathname.startsWith('/ws') || url.pathname === '/healthz') return
  if (request.mode === 'navigate') {
    event.respondWith(caches.open(CACHE).then(async (cache) => {
      const shell = await cache.match('/index.html')
      if (!shell) return fetch(request)
      // Go's file server redirects /index.html to /. Browsers reject a cached
      // redirected response for navigation (whose redirect mode is manual).
      // Preserve its body and security headers, clearing only redirect metadata.
      return shell.redirected ? new Response(shell.body, {
        status: shell.status, statusText: shell.statusText, headers: shell.headers,
      }) : shell
    }))
    return
  }
  if (!publicPaths.has(url.pathname) && !url.pathname.startsWith('/assets/')) return
  event.respondWith((async () => {
    const cache = await caches.open(CACHE)
    const cached = await cache.match(url.pathname)
    if (cached) return cached
    // Only immutable assets may be recovered from an older app version.
    if (url.pathname.startsWith('/assets/')) {
      for (const key of await caches.keys()) {
        if (!/^herdrx-(shell|assets)-/.test(key)) continue
        const previous = await (await caches.open(key)).match(url.pathname)
        if (previous) return previous
      }
    }
    return fetch(request)
  })())
})

function notificationTarget(value) {
  try {
    const url = new URL(value || '/', self.location.origin)
    // Notifications only navigate to a workbench or the host list.
    if (url.origin === self.location.origin && /^\/h\/[^/]+$/.test(url.pathname)) return url.pathname
  } catch {}
  return '/'
}

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const target = notificationTarget(event.notification.data?.url)
  event.waitUntil(self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then(async (clients) => {
    // Focusing another terminal must not navigate it away from unsaved input.
    const existing = clients.find((client) => new URL(client.url).pathname === target)
    if (existing) return existing.focus()
    return self.clients.openWindow(target)
  }))
})
self.addEventListener('push', (event) => {
  let payload = { title: 'herdrx', body: 'Agent 状态已更新。', tag: 'herdrx', data: { url: '/' } }
  try { payload = { ...payload, ...event.data.json() } } catch {}
  event.waitUntil(self.registration.showNotification(String(payload.title), {
    body: String(payload.body), tag: String(payload.tag),
    data: { url: notificationTarget(payload.data?.url) },
  }))
})
