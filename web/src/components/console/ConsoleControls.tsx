import { memo, useCallback, useState } from 'react'
import { useConsoleControls } from '../../lib/console/useConsoleControls'
import { useConsoleLive } from '../../lib/console/useConsoleLive'
import type { ChatRecord } from '../../lib/structuredChatTypes'
import type { WorkbenchClient } from '../../lib/workbench'
import type { Pane } from '../../types'
import { ConsoleStatusBar } from './ConsoleStatusBar'
import { InteractionPanel } from './InteractionPanel'

/**
 * 对话控制台输入框上方的一组：运行状态栏与交互卡片。读屏状态只在这里流动，画面每变一次
 * 只重渲染这一小块，不带着整条对话流和输入框一起重渲染。只对 Claude pane 读屏。
 * 没有展开的卡片时标上 `data-dock`：空闲的模式胶囊停到下面输入区的工具行里，不单独占一行。
 */
export const ConsoleControls = memo(function ConsoleControls({ hostID, pane, client, records, onActiveChange, onOpenShell }: {
  hostID: string
  pane: Pane
  client: WorkbenchClient
  records: readonly ChatRecord[]
  /** 交互卡片展开（顶替输入框）时为 true。 */
  onActiveChange: (active: boolean) => void
  onOpenShell: () => void
}) {
  const live = useConsoleLive({ hostID, paneID: pane.pane_id, client, enabled: pane.agent === 'claude' })
  const controls = useConsoleControls({ client, hostID, paneID: pane.pane_id, mode: live.mode })
  const [expanded, setExpanded] = useState(false)
  const [dialog, setDialog] = useState(false)
  const report = useCallback((active: boolean) => {
    setExpanded(active)
    onActiveChange(active)
  }, [onActiveChange])
  return <div className="chat-console-controls" data-dock={!expanded || undefined}>
    <ConsoleStatusBar live={live} working={pane.agent_status === 'working'} locked={dialog ? '终端正弹着对话框，先处理它再切换模式或中断' : undefined} onInterrupt={controls.interrupt} onCycleMode={controls.cycleMode}/>
    <InteractionPanel hostID={hostID} pane={pane} records={records} live={live} onActiveChange={report} onDialogChange={setDialog} onOpenShell={onOpenShell}/>
  </div>
})
