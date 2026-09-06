import { Children, isValidElement, useId, useRef, type ReactNode, type ButtonHTMLAttributes } from 'react'
import * as Primitive from '@radix-ui/react-select'
import { Check, ChevronDown, ChevronUp } from 'lucide-react'
import { useFieldError } from './Form'

type OptionProps = { value?: string; disabled?: boolean; children: ReactNode }
export function SelectOption(_props: OptionProps) { return null }
type Props = Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'value' | 'onChange' | 'defaultValue'> & {
  value: string; required?: boolean; onChange: (event: { target: { value: string } }) => void
}
const encode = (value: string) => `value:${value}`
const optionText = (children: ReactNode): string => Children.toArray(children).map((child) => isValidElement<{ children?: ReactNode }>(child) ? optionText(child.props.children) : String(child)).join('')

// The native form control created internally by Radix is hidden; all visible UI is rendered here.
export function Select({ value, onChange, children, className = '', required, name, id, disabled, ...props }: Props) {
  const generated = useId(), fieldID = id || generated, error = useFieldError(fieldID)
  const trigger = useRef<HTMLButtonElement>(null)
  const options = Children.toArray(children).filter(isValidElement<OptionProps>).map((child) => ({ ...child.props, value: child.props.value ?? optionText(child.props.children) }))
  return <><Primitive.Root value={encode(value)} disabled={disabled} onValueChange={(next) => {
    if (!next.startsWith('value:')) return
    onChange({ target: { value: next.slice(6) } })
    trigger.current?.dispatchEvent(new Event('input', { bubbles: true }))
  }}><Primitive.Trigger {...props} ref={trigger} id={fieldID} className={`input select-trigger ${className}`} data-value={value} data-select-required={Boolean(required)} aria-required={required || undefined} aria-invalid={error ? true : props['aria-invalid']} aria-describedby={[props['aria-describedby'], error ? `${fieldID}-validation` : ''].filter(Boolean).join(' ') || undefined}>
    <Primitive.Value/><Primitive.Icon className="select-icon"><ChevronDown size={16}/></Primitive.Icon>
  </Primitive.Trigger><Primitive.Portal><Primitive.Content className="select-content" position="popper" sideOffset={5} collisionPadding={12} data-ui-overlay="select">
    <Primitive.ScrollUpButton className="select-scroll"><ChevronUp size={16}/></Primitive.ScrollUpButton>
    <Primitive.Viewport className="select-viewport">{options.map((option) => <Primitive.Item key={option.value} value={encode(option.value)} disabled={option.disabled} textValue={optionText(option.children)} className="select-option" data-value={option.value}>
      <Primitive.ItemText>{option.children}</Primitive.ItemText><Primitive.ItemIndicator className="select-check"><Check size={16}/></Primitive.ItemIndicator>
    </Primitive.Item>)}</Primitive.Viewport>
    <Primitive.ScrollDownButton className="select-scroll"><ChevronDown size={16}/></Primitive.ScrollDownButton>
  </Primitive.Content></Primitive.Portal></Primitive.Root>{name && <input type="hidden" name={name} value={value} disabled={disabled}/>} {error && <span className="field-error" id={`${fieldID}-validation`} role="alert">{error}</span>}</>
}
