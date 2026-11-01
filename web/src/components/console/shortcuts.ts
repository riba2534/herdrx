import { useEffect, useRef } from 'react'

// 卡片的数字键快捷作答。焦点在输入框、文本区（含终端的隐藏 textarea）或输入法组字时不抢键；
// 同时挂着几张卡片时只有最后挂上的那张接键，免得一次按键答了两个 pane。
const owners: object[] = []

function editable(target: EventTarget | null) {
  return target instanceof Element && Boolean(target.closest('input, textarea, select, [contenteditable=""], [contenteditable="true"]'))
}

/** `onDigit` 返回 true 表示这个数字有对应的动作（随后阻止默认行为）。 */
export function useDigitShortcuts(enabled: boolean, onDigit: (digit: number) => boolean) {
  const latest = useRef(onDigit)
  latest.current = onDigit
  useEffect(() => {
    if (!enabled) return
    const owner = {}
    owners.push(owner)
    const handler = (event: KeyboardEvent) => {
      if (owners.at(-1) !== owner || event.defaultPrevented || event.repeat || event.isComposing || event.keyCode === 229) return
      if (event.ctrlKey || event.metaKey || event.altKey || !/^[1-9]$/.test(event.key) || editable(event.target)) return
      if (latest.current(Number(event.key))) event.preventDefault()
    }
    window.addEventListener('keydown', handler)
    return () => {
      window.removeEventListener('keydown', handler)
      owners.splice(owners.indexOf(owner), 1)
    }
  }, [enabled])
}
