import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'

export type ContextMenuItem = {
  id: string
  label: string
  icon?: ReactNode
  disabled?: boolean
  danger?: boolean
  separatorBefore?: boolean
  onSelect: () => void
}

export function ContextMenu({ x, y, label, items, onClose }: { x: number; y: number; label: string; items: ContextMenuItem[]; onClose: () => void }) {
  const menuRef = useRef<HTMLDivElement>(null)
  const [position, setPosition] = useState({ left: x, top: y })

  useLayoutEffect(() => {
    const menu = menuRef.current
    if (!menu) return
    const rect = menu.getBoundingClientRect()
    const gutter = 8
    setPosition({
      left: Math.max(gutter, Math.min(x, window.innerWidth - rect.width - gutter)),
      top: Math.max(gutter, Math.min(y, window.innerHeight - rect.height - gutter)),
    })
    menu.querySelector<HTMLButtonElement>('button:not(:disabled)')?.focus()
  }, [x, y, items])

  useEffect(() => {
    const pointerDown = (event: PointerEvent) => {
      if (!menuRef.current?.contains(event.target as Node)) onClose()
    }
    const close = () => onClose()
    document.addEventListener('pointerdown', pointerDown, true)
    window.addEventListener('blur', close)
    window.addEventListener('resize', close)
    window.addEventListener('scroll', close, true)
    return () => {
      document.removeEventListener('pointerdown', pointerDown, true)
      window.removeEventListener('blur', close)
      window.removeEventListener('resize', close)
      window.removeEventListener('scroll', close, true)
    }
  }, [onClose])

  const moveFocus = (direction: 1 | -1) => {
    const buttons = [...(menuRef.current?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)') || [])]
    if (!buttons.length) return
    const current = buttons.indexOf(document.activeElement as HTMLButtonElement)
    buttons[(current + direction + buttons.length) % buttons.length].focus()
  }

  return <div
    ref={menuRef}
    className="context-menu"
    role="menu"
    aria-label={label}
    style={position}
    onContextMenu={(event) => event.preventDefault()}
    onKeyDown={(event) => {
      if (event.key === 'Escape') { event.preventDefault(); onClose() }
      else if (event.key === 'ArrowDown') { event.preventDefault(); moveFocus(1) }
      else if (event.key === 'ArrowUp') { event.preventDefault(); moveFocus(-1) }
      else if (event.key === 'Home') { event.preventDefault(); menuRef.current?.querySelector<HTMLButtonElement>('button:not(:disabled)')?.focus() }
      else if (event.key === 'End') { event.preventDefault(); [...(menuRef.current?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)') || [])].at(-1)?.focus() }
    }}
  >
    {items.map((item) => <div className={item.separatorBefore ? 'context-menu-entry context-menu-separated' : 'context-menu-entry'} key={item.id}>
      <button
        type="button"
        role="menuitem"
        className={item.danger ? 'context-menu-item context-menu-danger' : 'context-menu-item'}
        disabled={item.disabled}
        onClick={() => { item.onSelect(); onClose() }}
      >
        <span className="context-menu-icon" aria-hidden="true">{item.icon}</span>
        <span>{item.label}</span>
      </button>
    </div>)}
  </div>
}
