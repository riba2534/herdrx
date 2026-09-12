// Apply the saved appearance before the application and its stylesheet paint.
// manifest.webmanifest keeps the site-default theme_color (#f7f8fa). This script
// and appearance.ts update the theme-color meta at runtime for the active scope
// (workbench defaults to dark #142c3c).
(() => {
const workbench = /^\/h\/[^/]+$/.test(location.pathname) && !location.hash.startsWith('#pair=')
let choice = workbench ? 'dark' : 'light'
try {
  const saved = localStorage.getItem(workbench ? 'herdrx.workbench-appearance.v1' : 'herdrx.site-appearance.v1')
  if (saved === 'dark' || saved === 'light' || (workbench && saved === 'solarized-light')) choice = saved
} catch { /* Use the section's default when storage is unavailable. */ }
const dark = choice === 'dark'
document.documentElement.dataset.appearanceScope = workbench ? 'workbench' : 'site'
document.documentElement.dataset.appearance = choice
document.documentElement.style.colorScheme = dark ? 'dark' : 'light'
const background = dark ? '#142c3c' : choice === 'solarized-light' ? '#fdf6e3' : '#f7f8fa'
document.documentElement.style.backgroundColor = background
document.querySelector('meta[name="theme-color"]')?.setAttribute('content', background)

const bootSlowMs = 3000
const bootFailAfterLoadMs = 8000
let bootPainted = ''

function bootReady() {
  if (window.__herdrxBooted) return true
  const root = document.getElementById('root')
  return Boolean(root && root.childElementCount > 0 && !root.querySelector('[data-boot-fallback]'))
}

function paintBootFallback(kind) {
  if (bootReady() || bootPainted === 'fail') return
  if (kind === 'slow' && bootPainted === 'slow') return
  const root = document.getElementById('root')
  if (!root) return
  const dark = document.documentElement.dataset.appearance === 'dark'
  const main = document.createElement('main')
  main.dataset.bootFallback = kind
  main.style.cssText = `min-height:100dvh;display:grid;place-items:center;padding:24px;font-family:system-ui;color:${dark ? '#e2ecf0' : '#1f2937'};background:${dark ? '#142c3c' : '#f7f8fa'}`
  const card = document.createElement('section')
  card.style.cssText = 'max-width:420px;text-align:center'
  const title = document.createElement('h1')
  const detail = document.createElement('p')
  detail.style.color = dark ? '#abc0ca' : '#5b6472'
  if (kind === 'slow') {
    title.textContent = '网络较慢，仍在加载…'
    detail.textContent = '正在下载页面资源，请稍候。'
    card.append(title, detail)
  } else {
    title.textContent = '资源加载失败'
    detail.textContent = '浏览器可能保留了旧版本缓存。清理本站缓存不会删除服务端账号或主机数据。'
    const button = document.createElement('button')
    button.textContent = '清理缓存并重新加载'
    button.style.cssText = `min-height:44px;padding:10px 16px;border:0;border-radius:6px;background:${dark ? '#dbc37e' : '#3766a3'};color:${dark ? '#203343' : '#f7f9fc'};font-weight:500;cursor:pointer`
    button.addEventListener('click', async () => {
      const registrations = await navigator.serviceWorker?.getRegistrations?.() || []
      await Promise.all(registrations.filter((registration) => registration.scope === `${location.origin}/`).map((registration) => registration.unregister()))
      if ('caches' in window) await Promise.all((await caches.keys()).filter((key) => /^herdrx-(shell|assets)-/.test(key)).map((key) => caches.delete(key)))
      window.location.reload()
    })
    card.append(title, detail, button)
  }
  main.append(card)
  root.replaceChildren(main)
  bootPainted = kind
}

window.addEventListener('error', (event) => {
  const target = event.target
  if (target && target.tagName === 'SCRIPT' && target.src) paintBootFallback('fail')
}, true)

window.setTimeout(() => {
  if (!bootReady() && document.readyState !== 'complete') paintBootFallback('slow')
}, bootSlowMs)

const afterLoad = () => {
  if (bootReady()) return
  paintBootFallback('slow')
  window.setTimeout(() => { if (!bootReady()) paintBootFallback('fail') }, bootFailAfterLoadMs)
}
if (document.readyState === 'complete') afterLoad()
else window.addEventListener('load', afterLoad, { once: true })
})()
