export function shouldPreloadWorkbench(pathname: string) {
  return pathname.startsWith('/h/')
}

export function preloadWorkbenchChunk(pathname = window.location.pathname) {
  if (shouldPreloadWorkbench(pathname)) void import('../pages/WorkbenchPage')
}
