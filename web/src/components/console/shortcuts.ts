import { useEffect, useRef } from 'react'
import { editableFocused } from '../../lib/displayPreferences'

// 卡片的数字键快捷作答。焦点在输入框、文本区（含终端的隐藏 textarea）或输入法组字时不抢键；
// 同时挂着几张卡片时只有最后挂上的那张接键，免得一次按键答了两个 pane。
const owners: object[] = []

/** 按下的数字是 `indices` 之一时调用 `choose`。 */
export function useDigitShortcuts(enabled: boolean, indices: readonly number[], choose: (index: number) => void) {
  const latest = useRef({ indices, choose })
  latest.current = { indices, choose }
  useEffect(() => {
    if (!enabled) return
    const owner = {}
    owners.push(owner)
    const handler = (event: KeyboardEvent) => {
      if (owners.at(-1) !== owner || event.defaultPrevented || event.repeat || event.isComposing || event.key === 'Process' || event.keyCode === 229) return
      if (event.ctrlKey || event.metaKey || event.altKey || !/^[1-9]$/.test(event.key)) return
      if (editableFocused(event.target instanceof Element ? event.target : null)) return
      const digit = Number(event.key)
      if (!latest.current.indices.includes(digit)) return
      event.preventDefault()
      latest.current.choose(digit)
    }
    window.addEventListener('keydown', handler)
    return () => {
      window.removeEventListener('keydown', handler)
      owners.splice(owners.indexOf(owner), 1)
    }
  }, [enabled])
}
