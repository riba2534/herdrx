import { useRef, type KeyboardEvent, type ReactNode } from 'react'
import './workbench-ui.css'

export type SegmentedOption<T extends string> = {
  value: T
  label: ReactNode
  /** Accessible name and tooltip; defaults to the visible label. */
  hint?: string
  disabled?: boolean
}

/**
 * 单选组的方向键导航：在可选的下标间循环，Home / End 到两端；不是导航键时返回 undefined。
 */
export function rovingIndex(key: string, index: number, enabled: number[]) {
  if (key === 'Home') return enabled[0]
  if (key === 'End') return enabled.at(-1)
  const step = key === 'ArrowRight' || key === 'ArrowDown' ? 1 : key === 'ArrowLeft' || key === 'ArrowUp' ? -1 : 0
  return step && enabled.length ? enabled[(enabled.indexOf(index) + step + enabled.length) % enabled.length] : undefined
}

/**
 * 单选分段控件：role="radiogroup"，方向键在可用选项间循环并立即选中，Home / End 到两端，
 * Tab 只停在当前选项上。选项文字单行不换行。
 */
export function Segmented<T extends string>({ label, value, options, onChange, className = '' }: {
  label: string
  value: T
  options: SegmentedOption<T>[]
  onChange: (value: T) => void
  className?: string
}) {
  const refs = useRef<Array<HTMLButtonElement | null>>([])
  const enabled = options.map((option, index) => option.disabled ? -1 : index).filter((index) => index >= 0)
  const focusable = options.findIndex((option) => option.value === value && !option.disabled)
  const onKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const next = rovingIndex(event.key, index, enabled)
    if (next === undefined) return
    event.preventDefault()
    refs.current[next]?.focus()
    if (options[next].value !== value) onChange(options[next].value)
  }
  return <div role="radiogroup" aria-label={label} className={`segmented ${className}`}>
    {options.map((option, index) => {
      const checked = option.value === value
      return <button
        key={option.value}
        ref={(element) => { refs.current[index] = element }}
        type="button"
        role="radio"
        aria-checked={checked}
        aria-label={option.hint}
        data-tooltip={option.hint}
        disabled={option.disabled}
        tabIndex={index === (focusable >= 0 ? focusable : enabled[0]) ? 0 : -1}
        onClick={() => { if (!checked) onChange(option.value) }}
        onKeyDown={(event) => onKeyDown(event, index)}
      >{option.label}</button>
    })}
  </div>
}
