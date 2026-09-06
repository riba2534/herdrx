import { Input } from './Form'
import { Select, SelectOption } from './Select'
import { Minus, Plus, RotateCcw, Scan } from 'lucide-react'
import type { TerminalDisplay } from '../lib/displayPreferences'
import { Button } from './ui'

export function DisplayToolbar({ display, actualFontSize, onChange, onSettings }: {
  display: TerminalDisplay; actualFontSize: number; onChange: (patch: Partial<TerminalDisplay>) => void; onSettings: () => void
}) {
  return <div className="display-toolbar" role="toolbar" aria-label="终端显示">
    <Button className="tool-button display-font" onClick={onSettings} data-tooltip="调整终端字号" aria-label="调整终端字号">Aa <span>{actualFontSize.toFixed(1).replace(/\.0$/, '')} px</span></Button>
    <div className="display-zoom">
      <Button className="tool-button" disabled={display.zoom <= 50} aria-label="缩小终端" onClick={() => onChange({ zoom: Math.max(50, display.zoom - 10) })}><Minus size={14}/></Button>
      <Button className="tool-button display-percent" aria-label="重置终端缩放" data-tooltip="重置缩放到 100%" onClick={() => onChange({ zoom: 100 })}>{display.zoom}%</Button>
      <Button className="tool-button" disabled={display.zoom >= 200} aria-label="放大终端" onClick={() => onChange({ zoom: Math.min(200, display.zoom + 10) })}><Plus size={14}/></Button>
    </div>
    <Button className={`tool-button display-fit ${display.mode === 'fit' && display.zoom === 100 ? 'tool-button-active' : ''}`} aria-pressed={display.mode === 'fit' && display.zoom === 100} data-tooltip="缩放到完整显示当前终端" onClick={() => onChange({ mode: 'fit', zoom: 100 })}><Scan size={14}/>适应窗口</Button>
  </div>
}

export function DisplaySettings({ display, mobile, onChange, onReset }: {
  display: TerminalDisplay; mobile: boolean; onChange: (patch: Partial<TerminalDisplay>) => void; onReset: () => void
}) {
  return <fieldset className="display-settings">
    <legend>字号与缩放</legend>
    <p className="field-hint">自动保存在此浏览器，电脑和手机布局分别记忆。</p>
    <label className="field"><span className="field-label">显示方式</span><Select aria-label="显示方式" className="input" value={display.mode} onChange={(event) => onChange({ mode: event.target.value as TerminalDisplay['mode'], zoom: 100 })}><SelectOption value="fit">适应窗口 · 查看完整终端</SelectOption><SelectOption value="fixed">固定字号 · 滑动查看内容</SelectOption></Select></label>
    <label className="field"><span className="range-label"><span className="field-label">终端字号</span><output>{display.fontSize} px</output></span><Input type="range" min={10} max={28} step={1} value={display.fontSize} aria-label="终端字号" onChange={(event) => onChange({ fontSize: Number(event.target.value), mode: 'fixed' })}/></label>
    <label className="field"><span className="range-label"><span className="field-label">终端缩放</span><output>{display.zoom}%</output></span><Input type="range" min={50} max={200} step={10} value={display.zoom} aria-label="终端缩放" onChange={(event) => onChange({ zoom: Number(event.target.value) })}/></label>
    <p className="field-hint">放大后可{mobile ? '单指滑动' : '使用滚动条或触控板'}查看完整画面。浏览器的页面缩放和双指缩放仍可使用。</p>
    <Button className="button-secondary" onClick={onReset}><RotateCcw size={14}/>恢复显示默认值</Button>
  </fieldset>
}
