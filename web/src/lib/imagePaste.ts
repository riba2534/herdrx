export const MAX_IMAGE_SIZE = 20 * 1024 * 1024

// Prefer items for screenshots; fall back to files for OS file copies/drops.
// Do not read both representations: browsers can expose the same file twice.
export function clipboardImages(data: DataTransfer | null): File[] {
  if (!data) return []
  const isImage = (file: File) => file.type.startsWith('image/') || /\.(png|jpe?g|webp|gif)$/i.test(file.name)
  const items = Array.from(data.items || [])
    .filter((item) => item.kind === 'file')
    .map((item) => item.getAsFile())
    .filter((file): file is File => file !== null && isImage(file))
  return items.length ? items : Array.from(data.files || []).filter(isImage)
}

export function ownsImagePaste(host: HTMLElement, active: boolean, event: ClipboardEvent): boolean {
  if (event.defaultPrevented) return false
  const target = event.target
  if (target instanceof Node && host.contains(target)) return true
  if (!active) return false
  if (target instanceof Element && target.closest('.terminal-pane, input, textarea, select, [contenteditable]:not([contenteditable="false"]), [role="dialog"], [role="menu"]')) return false
  // Keep clipboard data out of a background terminal while a dialog is open.
  return !document.querySelector('[aria-modal="true"]')
}
