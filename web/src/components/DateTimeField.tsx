import { useId, useRef, useState, type KeyboardEvent } from 'react'
import * as Popover from '@radix-ui/react-popover'
import { CalendarDays, ChevronLeft, ChevronRight } from 'lucide-react'
import { Button } from './ui'
import { Input } from './Form'
import { Select, SelectOption } from './Select'

const pad = (n: number) => String(n).padStart(2, '0')
const stamp = (date: Date) => `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
export function DateTimeField({ label, value, onChange }: { label: string; value: string; onChange: (event: { target: { value: string } }) => void }) {
  const id = useId(), grid = useRef<HTMLDivElement>(null)
  const [open, setOpen] = useState(false), [day, setDay] = useState(() => new Date())
  const [month, setMonth] = useState(() => new Date()), [hour, setHour] = useState('00'), [minute, setMinute] = useState('00')
  const [focused, setFocused] = useState(() => stamp(new Date()))
  const start = new Date(month.getFullYear(), month.getMonth(), 1)
  start.setDate(1 - (start.getDay() + 6) % 7)
  const dates = Array.from({ length: 42 }, (_, index) => { const date = new Date(start); date.setDate(date.getDate() + index); return date })
  const validTime = /^\d{1,2}$/.test(hour) && Number(hour) <= 23 && /^\d{1,2}$/.test(minute) && Number(minute) <= 59
  const setShownMonth = (next: Date) => { setMonth(next); setFocused(stamp(new Date(next.getFullYear(), next.getMonth(), 1))) }
  const openChange = (next: boolean) => {
    if (next) {
      const date = value ? new Date(value) : new Date()
      setDay(date); setMonth(date); setFocused(stamp(date)); setHour(value ? pad(date.getHours()) : '00'); setMinute(value ? pad(date.getMinutes()) : '00')
    }
    setOpen(next)
  }
  const move = (event: KeyboardEvent, date: Date) => {
    const shifts: Record<string, number> = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -7, ArrowDown: 7, Home: -(date.getDay() + 6) % 7, End: 6 - (date.getDay() + 6) % 7 }
    const next = new Date(date)
    if (event.key in shifts) next.setDate(date.getDate() + shifts[event.key])
    else if (event.key === 'PageUp' || event.key === 'PageDown') { next.setDate(1); next.setMonth(next.getMonth() + (event.key === 'PageUp' ? -1 : 1)) }
    else return
    event.preventDefault(); setFocused(stamp(next)); setMonth(next)
    requestAnimationFrame(() => grid.current?.querySelector<HTMLButtonElement>(`[data-date="${stamp(next)}"]`)?.focus())
  }
  return <div className="field"><label className="field-label" htmlFor={id}>{label}</label><Popover.Root open={open} onOpenChange={openChange}><Popover.Trigger asChild><button id={id} type="button" className="input datetime-trigger" aria-label={label} data-value={value}><span>{value ? value.replace('T', ' ') : '选择日期和时间'}</span><CalendarDays size={16}/></button></Popover.Trigger><Popover.Portal><Popover.Content className="date-popover" sideOffset={6} collisionPadding={12} aria-label={`${label}选择器`} data-ui-overlay="calendar" onOpenAutoFocus={(event) => { event.preventDefault(); grid.current?.querySelector<HTMLButtonElement>('[tabindex="0"]')?.focus() }}>
    <div className="calendar-heading"><Button type="button" className="icon-button" aria-label="上个月" onClick={() => setShownMonth(new Date(month.getFullYear(), month.getMonth() - 1, 1))}><ChevronLeft size={17}/></Button><Select aria-label="年份" value={String(month.getFullYear())} onChange={(event) => setShownMonth(new Date(Number(event.target.value), month.getMonth(), 1))}>{Array.from({ length: 201 }, (_, index) => <SelectOption key={index} value={String(1900 + index)}>{1900 + index} 年</SelectOption>)}</Select><Select aria-label="月份" value={String(month.getMonth())} onChange={(event) => setShownMonth(new Date(month.getFullYear(), Number(event.target.value), 1))}>{Array.from({ length: 12 }, (_, index) => <SelectOption key={index} value={String(index)}>{index + 1} 月</SelectOption>)}</Select><Button type="button" className="icon-button" aria-label="下个月" onClick={() => setShownMonth(new Date(month.getFullYear(), month.getMonth() + 1, 1))}><ChevronRight size={17}/></Button></div>
    <div className="calendar-grid" role="grid" aria-label={`${month.getFullYear()} 年 ${month.getMonth() + 1} 月`} ref={grid}><div role="row" className="calendar-week">{'一二三四五六日'.split('').map((text) => <span role="columnheader" key={text}>{text}</span>)}</div>{Array.from({ length: 6 }, (_, row) => <div className="calendar-week" role="row" key={row}>{dates.slice(row * 7, row * 7 + 7).map((date) => <div role="gridcell" key={stamp(date)} aria-selected={stamp(date) === stamp(day)}><button type="button" className="calendar-day" data-date={stamp(date)} data-outside={date.getMonth() !== month.getMonth()} data-selected={stamp(date) === stamp(day)} aria-label={stamp(date)} aria-current={stamp(date) === stamp(new Date()) ? 'date' : undefined} tabIndex={stamp(date) === focused ? 0 : -1} onKeyDown={(event) => move(event, date)} onClick={() => { setDay(date); setFocused(stamp(date)); setMonth(date) }}>{date.getDate()}</button></div>)}</div>)}</div>
    <div className="calendar-time"><span>时间</span><Input aria-label="小时" className="input" inputMode="numeric" maxLength={2} value={hour} onChange={(event) => setHour(event.target.value)} onBlur={() => { if (/^\d{1,2}$/.test(hour)) setHour(pad(Number(hour))) }}/><span>:</span><Input aria-label="分钟" className="input" inputMode="numeric" maxLength={2} value={minute} onChange={(event) => setMinute(event.target.value)} onBlur={() => { if (/^\d{1,2}$/.test(minute)) setMinute(pad(Number(minute))) }}/><span className="field-hint">本地时间</span></div>
    {!validTime && <p className="field-error" role="alert">小时填写 0–23，分钟填写 0–59。</p>}
    <div className="calendar-actions"><Button type="button" className="button-ghost" onClick={() => { onChange({ target: { value: '' } }); setOpen(false) }}>清空</Button><Button type="button" className="button-secondary" onClick={() => setOpen(false)}>取消</Button><Button type="button" className="button-primary" disabled={!validTime} onClick={() => { onChange({ target: { value: `${stamp(day)}T${pad(Number(hour))}:${pad(Number(minute))}` } }); setOpen(false) }}>应用时间</Button></div>
  </Popover.Content></Popover.Portal></Popover.Root></div>
}
