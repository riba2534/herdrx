import { createContext, useContext, useId, useState, type FormHTMLAttributes, type ComponentPropsWithRef } from 'react'

const Validation = createContext<Record<string, string>>({})
export const useFieldError = (id: string) => useContext(Validation)[id]

function problem(field: HTMLInputElement | HTMLTextAreaElement) {
  const { validity: v, value } = field
  if (v.valueMissing) return '请填写此项。'
  if (v.typeMismatch) return field.type === 'email' ? '请输入完整的邮箱地址，例如 name@example.com。' : '请输入有效的地址。'
  if (v.badInput) return '请输入有效的数值。'
  if (v.rangeUnderflow) return `请输入不小于 ${(field as HTMLInputElement).min} 的数值。`
  if (v.rangeOverflow) return `请输入不大于 ${(field as HTMLInputElement).max} 的数值。`
  if (v.stepMismatch) return '请输入符合步长要求的数值。'
  if (v.patternMismatch) return field.dataset.formatHint || '请按要求的格式填写。'
  if (value && field.minLength > 0 && value.length < field.minLength) return `请至少输入 ${field.minLength} 个字符。`
  if (v.tooLong) return `请最多输入 ${field.maxLength} 个字符。`
  if (v.customError) return '请检查此项内容。'
  return ''
}

export function Form({ children, onSubmit, onInput, ...props }: FormHTMLAttributes<HTMLFormElement>) {
  const [errors, setErrors] = useState<Record<string, string>>({})
  return <Validation.Provider value={errors}><form {...props} noValidate onInput={(event) => {
    const id = (event.target as HTMLElement).id
    if (id) setErrors((current) => { if (!current[id]) return current; const next = { ...current }; delete next[id]; return next })
    onInput?.(event)
  }} onSubmit={(event) => {
    event.preventDefault()
    const next: Record<string, string> = {}
    let first: HTMLElement | undefined
    for (const element of event.currentTarget.querySelectorAll<HTMLElement>('input, textarea, [data-select-required]')) {
      if (element instanceof HTMLInputElement || element instanceof HTMLTextAreaElement) {
        if (!element.willValidate) continue
        const message = problem(element)
        if (message) { next[element.id] = message; first ||= element }
      } else if (element.dataset.selectRequired === 'true' && element.dataset.value === '' && !element.hasAttribute('disabled')) {
        next[element.id] = '请选择此项。'; first ||= element
      }
    }
    setErrors(next)
    if (first) first.focus()
    else onSubmit?.(event)
  }}>{children}</form></Validation.Provider>
}

export function Input({ id, ...props }: ComponentPropsWithRef<'input'>) {
  const generated = useId(), fieldID = id || generated, error = useFieldError(fieldID)
  return <><input {...props} id={fieldID} aria-invalid={error ? true : props['aria-invalid']} aria-describedby={[props['aria-describedby'], error ? `${fieldID}-validation` : ''].filter(Boolean).join(' ') || undefined}/>{error && <span className="field-error" id={`${fieldID}-validation`} role="alert">{error}</span>}</>
}
export function Textarea({ id, ...props }: ComponentPropsWithRef<'textarea'>) {
  const generated = useId(), fieldID = id || generated, error = useFieldError(fieldID)
  return <><textarea {...props} id={fieldID} aria-invalid={error ? true : props['aria-invalid']} aria-describedby={[props['aria-describedby'], error ? `${fieldID}-validation` : ''].filter(Boolean).join(' ') || undefined}/>{error && <span className="field-error" id={`${fieldID}-validation`} role="alert">{error}</span>}</>
}
