import { Input } from './Form'
import { Select, SelectOption } from './Select'
import { Minus, Plus, RotateCcw, Scan } from 'lucide-react'
import type { DisplayProfileKey, TerminalDisplay } from '../lib/displayPreferences'
import { Button } from './ui'

const modeHint = { auto: '当前为自动，尽量完整显示当前终端或铺满宽度', fit: '当前为完整显示，完整显示当前终端', fixed: '当前为固定字号，超出部分可滚动' } as const
const profileName: Record<DisplayProfileKey, string> = { desktop: '电脑布局', mobile: '手机布局', mobileLandscape: '手机横屏' }

export function DisplayToolbar({ display, actualFontSize, onChange, onSettings }: {
  display: TerminalDisplay; actualFontSize: number; onChange: (patch: Partial<TerminalDisplay>) => void; onSettings: () => void
}) {
  const fitting = display.mode === 'fit' && display.zoom === 100
  const fitTooltip = display.mode === 'auto' ? '自动已按“字号 × 缩放”尽量完整显示；点击改为“完整显示”，字号可低于 10 px'
    : fitting ? '恢复固定字号，超出部分可滚动查看' : '缩放到完整显示当前终端'
  return <div className="display-toolbar" role="toolbar" aria-label="终端显示" data-display-mode={display.mode}>
    <Button className="tool-button display-font" onClick={onSettings} data-tooltip={`${modeHint[display.mode]} · 实际 ${actualFontSize.toFixed(1).replace(/\.0$/, '')} px（设置里的字号是上限）· 点击调整`} aria-label="调整终端字号">Aa <span>{actualFontSize.toFixed(1).replace(/\.0$/, '')} px</span></Button>
    <div className="display-zoom">
      <Button className="tool-button" disabled={display.zoom <= 50} aria-label="缩小终端" onClick={() => onChange({ zoom: Math.max(50, display.zoom - 10) })}><Minus size={14}/></Button>
      <Button className="tool-button display-percent" aria-label="重置终端缩放" data-tooltip="重置缩放到 100%" onClick={() => onChange({ zoom: 100 })}>{display.zoom}%</Button>
      <Button className="tool-button" disabled={display.zoom >= 200} aria-label="放大终端" onClick={() => onChange({ zoom: Math.min(200, display.zoom + 10) })}><Plus size={14}/></Button>
    </div>
    <Button className={`tool-button display-fit ${fitting ? 'tool-button-active' : ''}`} aria-pressed={fitting} data-tooltip={fitTooltip} onClick={() => onChange(fitting ? { mode: 'fixed', zoom: 100 } : { mode: 'fit', zoom: 100 })}><Scan size={14}/>完整显示</Button>
  </div>
}

export function DisplaySettings({ display, mobile, profile, onChange, onReset }: {
  display: TerminalDisplay; mobile: boolean; profile?: DisplayProfileKey; onChange: (patch: Partial<TerminalDisplay>) => void; onReset: () => void
}) {
  return <fieldset className="display-settings">
    <legend>字号与缩放{profile ? ` · ${profileName[profile]}` : ''}</legend>
    <p className="field-hint">字号与本地显示方式保存在此浏览器，电脑布局、手机布局和手机横屏分别记忆。默认“自动”：以“字号 × 缩放”为上限，能读清时完整显示或铺满宽度（上下滑动），否则保持字号并滑动查看。电脑默认 14 px，手机默认 13 px。打开网页不会调整远端尺寸。</p>
    <label className="field"><span className="field-label">本地显示</span><Select aria-label="显示方式" className="input" value={display.mode} onChange={(event) => onChange({ mode: event.target.value as TerminalDisplay['mode'], zoom: 100 })}><SelectOption value="auto">自动 · 尽量显示完整终端</SelectOption><SelectOption value="fit">完整显示 · 查看完整终端</SelectOption><SelectOption value="fixed">固定字号 · 滑动查看内容</SelectOption></Select></label>
    <label className="field"><span className="range-label"><span className="field-label">终端字号</span><output>{display.fontSize} px</output></span><Input type="range" min={10} max={28} step={1} value={display.fontSize} aria-label="终端字号" onChange={(event) => onChange({ fontSize: Number(event.target.value), mode: 'fixed' })}/></label>
    <label className="field"><span className="range-label"><span className="field-label">终端缩放</span><output>{display.zoom}%</output></span><Input type="range" min={50} max={200} step={10} value={display.zoom} aria-label="终端缩放" onChange={(event) => onChange({ zoom: Number(event.target.value) })}/></label>
    <p className="field-hint">{display.mode === 'auto' ? `自动模式以“字号 × 缩放”为上限缩小字号：先尽量完整显示当前终端，再尝试铺满宽度；缩到 ${mobile ? 9 : 10} px 仍放不下时，保持字号并可滑动查看。拖动字号滑杆会切换到固定字号。` : <>放大后可{mobile ? '单指滑动' : '使用滚动条或触控板'}查看完整画面。</>}{mobile ? ' 在终端画面上双指捏合可直接调整字号，捏到最小以下会切换为完整显示；页面其他区域仍可使用浏览器缩放。' : ' 浏览器的页面缩放仍可使用。'}</p>
    <Button className="button-secondary" onClick={onReset}><RotateCcw size={14}/>恢复显示默认值</Button>
  </fieldset>
}
