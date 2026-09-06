import { useEffect, useId, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

// Delegation avoids wrappers that would change compact toolbar and table layouts.
export function Tooltips() {
  const id = useId(), popup = useRef<HTMLDivElement>(null)
  const [tip, setTip] = useState<{ text: string; left: number; top: number; above: boolean } | null>(null)
  useEffect(() => {
    let current: HTMLElement | null = null, timer: ReturnType<typeof setTimeout> | undefined, previous: string | null = null
    const hide = () => {
      clearTimeout(timer); setTip(null)
      if (current) { if (previous === null) current.removeAttribute('aria-describedby'); else current.setAttribute('aria-describedby', previous) }
      current = null
    }
    const show = (event: Event) => {
      if (event instanceof PointerEvent && event.pointerType !== 'mouse') return
      const target = (event.target as Element)?.closest<HTMLElement>('[data-tooltip]')
      if (target === current) return
      hide()
      if (!target?.dataset.tooltip) return
      current = target; previous = target.getAttribute('aria-describedby')
      timer = setTimeout(() => {
        if (!target.isConnected) { hide(); return }
        const rect = target.getBoundingClientRect(), above = rect.top > 100
        target.setAttribute('aria-describedby', [previous, id].filter(Boolean).join(' '))
        setTip({ text: target.dataset.tooltip!, left: Math.max(12, Math.min(rect.left + rect.width / 2, window.innerWidth - 12)), top: above ? rect.top - 8 : rect.bottom + 8, above })
      }, event.type === 'focusin' ? 0 : 450)
    }
    const leave = (event: Event) => { if (current && !current.contains((event as FocusEvent).relatedTarget as Node | null)) hide() }
    const key = (event: KeyboardEvent) => { if (event.key === 'Escape') hide() }
    document.addEventListener('pointerover', show); document.addEventListener('focusin', show)
    document.addEventListener('pointerout', leave); document.addEventListener('focusout', leave)
    document.addEventListener('pointerdown', hide); document.addEventListener('keydown', key)
    window.addEventListener('scroll', hide, true); window.addEventListener('resize', hide)
    return () => { hide(); document.removeEventListener('pointerover', show); document.removeEventListener('focusin', show); document.removeEventListener('pointerout', leave); document.removeEventListener('focusout', leave); document.removeEventListener('pointerdown', hide); document.removeEventListener('keydown', key); window.removeEventListener('scroll', hide, true); window.removeEventListener('resize', hide) }
  }, [id])
  useLayoutEffect(() => {
    if (!tip || !popup.current) return
    const node = popup.current, rect = node.getBoundingClientRect()
    node.style.left = `${Math.max(12, Math.min(tip.left - rect.width / 2, window.innerWidth - rect.width - 12))}px`
    node.style.top = `${Math.max(12, Math.min(tip.above ? tip.top - rect.height : tip.top, window.innerHeight - rect.height - 12))}px`
  }, [tip])
  if (!tip) return null
  const width = Math.min(320, window.innerWidth - 24), left = Math.max(12, Math.min(tip.left - width / 2, window.innerWidth - width - 12))
  return createPortal(<div ref={popup} id={id} role="tooltip" className="ui-tooltip" style={{ left, top: tip.top, maxWidth: width }}>{tip.text}</div>, document.body)
}
