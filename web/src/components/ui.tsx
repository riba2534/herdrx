import { Input } from './Form'
import { useId, useState, type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactNode } from 'react'
import { Eye, EyeOff, LoaderCircle } from 'lucide-react'
import { agentStatusLabel } from '../lib/labels'

export function Button({ className = '', pending, children, title, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { pending?: boolean }) {
  return <button className={`button ${className}`} data-tooltip={title} {...props} disabled={pending || props.disabled}>
    {pending && <LoaderCircle aria-hidden="true" className="spin" size={16} />}{children}
  </button>
}

export function Field({ label, hint, error, type, ...props }: InputHTMLAttributes<HTMLInputElement> & { label: string; hint?: string; error?: string }) {
  const generatedID = useId()
  const id = props.id || props.name || generatedID
  const descriptionID = `${id}-description`
  const [visible, setVisible] = useState(false)
  const password = type === 'password'
  const input = <Input id={id} className={error ? 'input input-error' : 'input'} aria-invalid={Boolean(error)} aria-describedby={error || hint ? descriptionID : undefined} {...props} type={password && visible ? 'text' : type} />
  return <div className="field">
    <label className="field-label" htmlFor={id}>{label}</label>
    {password ? <div className="field-password">{input}<Button type="button" className="icon-button field-password-toggle" aria-label={visible ? '隐藏密码' : '显示密码'} aria-pressed={visible} onClick={() => setVisible((current) => !current)}>{visible ? <EyeOff size={16} aria-hidden="true"/> : <Eye size={16} aria-hidden="true"/>}</Button></div> : input}
    {error ? <span id={descriptionID} className="field-error" role="alert">{error}</span> : hint ? <span id={descriptionID} className="field-hint">{hint}</span> : null}
  </div>
}

export function EmptyState({ icon, title, detail, action }: { icon: ReactNode; title: string; detail: string; action?: ReactNode }) {
  return <div className="empty-state">{icon}<h2>{title}</h2><p>{detail}</p>{action}</div>
}

export function StatusDot({ status }: { status: string }) {
  const symbol = status === 'blocked' ? '×' : status === 'working' ? '◐' : status === 'done' ? '✓' : status === 'idle' ? '○' : '·'
  return <span className={`status-dot status-${status}`} role="img" aria-label={agentStatusLabel(status)}>{symbol}</span>
}
