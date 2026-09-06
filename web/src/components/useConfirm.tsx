import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { Modal } from './Modal'
import { Button } from './ui'

type Options = { title?: string; confirmLabel?: string; danger?: boolean; onConfirm?: () => void }
type Request = Options & { message: string }
export function useConfirm(scope?: unknown) {
  const [request, setRequest] = useState<Request | null>(null)
  const resolver = useRef<((accepted: boolean) => void) | null>(null)
  const mounted = useRef(true), id = useId()
  useEffect(() => { mounted.current = true; setRequest(null); return () => { mounted.current = false; resolver.current?.(false); resolver.current = null } }, [scope])
  const confirm = useCallback((message: string, options: Options = {}) => {
    if (!mounted.current || resolver.current) return Promise.resolve(false)
    return new Promise<boolean>((resolve) => { resolver.current = resolve; setRequest({ message, ...options }) })
  }, [])
  const finish = (accepted: boolean) => {
    const resolve = resolver.current
    if (!resolve) return
    resolver.current = null
    setRequest(null)
    // Keep link opening inside the click gesture, including in Safari.
    try { if (accepted) request?.onConfirm?.() } finally { resolve(accepted) }
  }
  const dialog = request && <Modal title={request.title || '确认操作'} role="alertdialog" descriptionID={id} onClose={() => finish(false)} className="confirm-modal">
    <p id={id} className="confirm-message">{request.message}</p><div className="modal-actions"><Button type="button" className="button-secondary" data-initial-focus onClick={() => finish(false)}>取消</Button><Button type="button" className={request.danger === false ? 'button-primary' : 'button-danger'} onClick={() => finish(true)}>{request.confirmLabel || '确认操作'}</Button></div>
  </Modal>
  return { confirm, dialog }
}
