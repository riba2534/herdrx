import type { WorkbenchMode } from '../../lib/workbenchMode'
import { Segmented, type SegmentedOption } from './Segmented'

const MODES: SegmentedOption<WorkbenchMode>[] = [
  { value: 'ui', label: 'UI', hint: 'Web UI 模式：对话与结构化操作' },
  { value: 'shell', label: 'Shell', hint: 'Web Shell 模式：完整终端' },
]

/** 工作台模式开关：两种模式看的是同一个 Herdr 的 workspace / tab / pane，切换不调用 Herdr。 */
export function ModeSwitch({ mode, onChange }: { mode: WorkbenchMode; onChange: (mode: WorkbenchMode) => void }) {
  return <Segmented label="工作台模式" value={mode} options={MODES} onChange={onChange} className="mode-switch"/>
}
