// Apply the saved appearance before the application and its stylesheet paint.
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

window.setTimeout(() => {
  const root = document.getElementById('root')
  if (!root || root.childElementCount > 0) return
  const main = document.createElement('main')
  const dark = document.documentElement.dataset.appearance === 'dark'
  main.style.cssText = `min-height:100dvh;display:grid;place-items:center;padding:24px;font-family:system-ui;color:${dark ? '#e2ecf0' : '#1f2937'};background:${dark ? '#142c3c' : '#f7f8fa'}`
  const card = document.createElement('section')
  card.style.cssText = 'max-width:420px;text-align:center'
  const title = document.createElement('h1')
  title.textContent = 'herdrx 页面资源需要刷新'
  const detail = document.createElement('p')
  detail.textContent = '浏览器可能保留了旧版本缓存。清理本站缓存不会删除服务端账号或主机数据。'
  detail.style.color = dark ? '#abc0ca' : '#5b6472'
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
  main.append(card)
  root.replaceChildren(main)
}, 3000)
