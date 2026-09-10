import { useEffect, useRef, useState, type ReactNode } from 'react'
import * as Dialog from '@radix-ui/react-dialog'
import { X } from 'lucide-react'
import { Button } from './ui'

export function Modal({ title, busy = false, allowCloseWhileBusy = false, onClose, children, className = '', role = 'dialog', descriptionID, closeLabel = '关闭' }: { title: string; busy?: boolean; allowCloseWhileBusy?: boolean; onClose: () => void; children: ReactNode; className?: string; role?: 'dialog' | 'alertdialog'; descriptionID?: string; closeLabel?: string }) {
  const canClose = !busy || allowCloseWhileBusy
  const measure = () => ({ height: window.visualViewport?.height || window.innerHeight, top: window.visualViewport?.offsetTop || 0 })
  const [viewport, setViewport] = useState(measure)
  useEffect(() => {
    const update = () => setViewport(measure())
    window.visualViewport?.addEventListener('resize', update); window.visualViewport?.addEventListener('scroll', update); window.addEventListener('resize', update)
    return () => { window.visualViewport?.removeEventListener('resize', update); window.visualViewport?.removeEventListener('scroll', update); window.removeEventListener('resize', update) }
  }, [])
  const previous = useRef(document.activeElement as HTMLElement | null)
  return <Dialog.Root open onOpenChange={(open) => { if (!open && canClose) onClose() }}><Dialog.Portal><Dialog.Overlay className={`modal-backdrop ${role === 'alertdialog' ? 'confirmation-backdrop' : ''}`}/>
    <Dialog.Content className={`modal ui-modal ${className}`} style={{ maxHeight: Math.max(100, viewport.height - 32), top: viewport.top + viewport.height / 2 }} role={role} aria-describedby={descriptionID} data-ui-overlay="dialog" onPointerDown={(event) => event.stopPropagation()} onOpenAutoFocus={(event) => {
      const target = event.currentTarget as HTMLElement
      const initial = target.querySelector<HTMLElement>('[data-initial-focus], [autofocus]')
      if (initial) { event.preventDefault(); initial.focus() }
    }} onCloseAutoFocus={(event) => { event.preventDefault(); if (previous.current?.isConnected) previous.current.focus() }} onEscapeKeyDown={(event) => { if (!canClose) event.preventDefault() }} onPointerDownOutside={(event) => { if (!canClose || role === 'alertdialog') event.preventDefault() }}>
      <div className="modal-header"><Dialog.Title asChild><h2>{title}</h2></Dialog.Title><Button type="button" className="icon-button" aria-label={closeLabel} disabled={!canClose} onClick={onClose}><X size={18}/></Button></div>
      {children}
    </Dialog.Content></Dialog.Portal></Dialog.Root>
}
