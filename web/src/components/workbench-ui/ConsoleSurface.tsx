import { useEffect, useMemo, useRef, useState, type ComponentType, type KeyboardEvent, type ReactNode } from 'react'
import type { ITheme } from '@xterm/xterm'
import { Clock3, MessageSquarePlus, SquareTerminal } from 'lucide-react'
import { ChatView, type ChatViewProps } from '../ChatView'
import { ScreenView } from '../ScreenView'
import { DialogCard, usePrimaryDialogCard } from '../DialogCard'
import { useConfirm } from '../useConfirm'
import { Button, StatusDot } from '../ui'
import { composerInFlight, composerStatusText, idleComposerSend, readComposerDraft, readComposerSend, runComposerPrompt, subscribeComposer, writeComposerDraft } from '../../lib/composerDrafts'
import { useAppearance } from '../../lib/appearance'
import { agentStatusLabel, paneDisplayName } from '../../lib/labels'
import { terminalThemes } from '../../lib/themes'
import type { WorkbenchClient } from '../../lib/workbench'
import type { Pane, PaneDialog } from '../../types'
import { cancelFirstMessage, useFirstMessageState } from './firstMessage'
import './workbench-ui.css'

/** Web UI 模式用对话视图显示的 pane（v1 只支持 Claude）；其余 pane 显示只读画面。 */
export const consoleShowsChat = (pane: Pane) => pane.agent === 'claude'

// F1 adds `variant` to ChatView; until that lands the prop goes through this widened type.
const ConsoleChatView = ChatView as ComponentType<ChatViewProps & { variant?: 'pane' | 'console' }>

/** 只读画面的配色：底色和正文跟随控制台，ANSI 色板按深浅外观取一套终端主题。 */
function useConsoleScreenTheme(): ITheme {
  const [appearance] = useAppearance('workbench')
  return useMemo(() => ({
    ...terminalThemes[appearance === 'dark' ? 'GitHub Dark' : 'Catppuccin Latte'],
    background: 'var(--console-code-bg)',
    foreground: 'var(--console-text)',
  }), [appearance])
}

/**
 * 非 Claude pane 的单行命令输入：与 Shell 模式的本地输入框共用同一份草稿和发送事务
 * （服务端读屏防护、固定的消息编号、结果不确定不重发）。Enter 发送，Shift+Enter 换行。
 */
function ConsoleCommandInput({ hostID, paneID, connected, onOpenInShell }: { hostID: string; paneID: string; connected: boolean; onOpenInShell: () => void }) {
  const [value, setValue] = useState(() => readComposerDraft(hostID, paneID))
  const [send, setSend] = useState(() => readComposerSend(hostID, paneID))
  // 发送因对话框被拒绝：显示答题卡片（工作台已有主卡片时不重复）。
  const [refusedDialog, setRefusedDialog] = useState<PaneDialog | null>()
  const primaryCard = usePrimaryDialogCard(hostID, paneID)
  const { confirm, dialog: confirmDialog } = useConfirm(`${hostID}\n${paneID}`)

  useEffect(() => subscribeComposer(() => {
    setValue(readComposerDraft(hostID, paneID))
    setSend(readComposerSend(hostID, paneID))
  }), [hostID, paneID])
  useEffect(() => {
    if (send.status !== 'delivered') return
    const timer = window.setTimeout(() => idleComposerSend(hostID, paneID), 3500)
    return () => window.clearTimeout(timer)
  }, [send.status, send.revision, hostID, paneID])

  // 确认框在发送结果回来之后才弹：按此刻的连接状态判断，不用发起时的快照。
  const connectedRef = useRef(connected)
  connectedRef.current = connected
  const sending = send.status === 'sending' || composerInFlight(hostID, paneID)
  const submit = (force = false) => {
    if (!connectedRef.current || !readComposerDraft(hostID, paneID).trim() || composerInFlight(hostID, paneID)) return
    setRefusedDialog(undefined)
    void runComposerPrompt(hostID, paneID, { force }).then(async (result) => {
      if (result.kind === 'dialog') setRefusedDialog(result.dialog)
      if (result.kind === 'draft' && await confirm('终端的命令行里已有没发出的内容。仍要发送的话，这段文字会接在那段内容后面一起提交。', { title: '终端里有未发送的输入，仍要发送吗？', confirmLabel: '仍要发送', danger: false })) submit(true)
    })
  }
  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.nativeEvent.isComposing || event.key === 'Process' || event.nativeEvent.keyCode === 229) return
    if (event.key !== 'Enter' || event.shiftKey || event.altKey || event.repeat) return
    event.preventDefault()
    submit()
  }
  return <div className="console-command" role="region" aria-label="命令输入">
    {refusedDialog !== undefined && !primaryCard && <DialogCard hostID={hostID} paneID={paneID} initial={refusedDialog} onClose={() => setRefusedDialog(undefined)}/>}
    <div className="console-command-row">
      <span className="console-command-prompt" aria-hidden="true">$</span>
      <textarea
        className="console-command-input"
        aria-label="发送到终端的命令"
        rows={1}
        value={value}
        placeholder={connected ? '输入一行命令，Enter 发送到这个终端' : '主机未连接，暂不能发送'}
        autoComplete="off"
        autoCorrect="off"
        autoCapitalize="off"
        spellCheck={false}
        enterKeyHint="send"
        onChange={(event) => { setValue(event.target.value); writeComposerDraft(hostID, paneID, event.target.value) }}
        onKeyDown={onKeyDown}
      />
      <Button className="console-button console-button-primary" disabled={!connected || !value.trim() || sending} pending={sending} onClick={() => submit()}>发送</Button>
    </div>
    <div className="console-command-meta">
      <span role="status" aria-live="polite">{send.status === 'idle' ? '只读画面；输入会整段发送到这个终端，不会自动重发。' : composerStatusText(send, true)}</span>
      <button type="button" className="console-link" onClick={onOpenInShell}><SquareTerminal size={13} aria-hidden="true"/>在 Shell 中打开</button>
    </div>
    {confirmDialog}
  </div>
}

/** 首条消息还在等 Claude 就绪：说明一下，并允许取消（原文放回输入框）。 */
function FirstMessageBanner({ hostID, paneID }: { hostID: string; paneID: string }) {
  const state = useFirstMessageState(hostID, paneID)
  if (!state) return null
  return <div className="console-banner" role="status">
    <Clock3 size={14} aria-hidden="true"/>
    <span>{state === 'sending' ? '正在发送首条消息…' : '首条消息会在 Claude 就绪、没有待答对话框时自动发送一次。'}</span>
    <button type="button" className="console-link" disabled={state === 'sending'} onClick={() => cancelFirstMessage(hostID, paneID)}>取消自动发送</button>
  </div>
}

export type ConsoleSurfaceProps = {
  hostID: string
  client: WorkbenchClient
  /** 当前标签页的 pane，按布局顺序。 */
  panes: Pane[]
  paneID: string
  onSelectPane: (paneID: string) => void
  compact: boolean
  connected: boolean
  connectionEpoch: number
  /** 还没收到第一份快照。 */
  loading: boolean
  /** 手机有自己的 pane 切换条，这里不再画一份。 */
  showPanePicker: boolean
  onOpenInShell: (paneID: string) => void
  onShell: () => void
  onNewSession: () => void
  /** 连接横幅与离线遮罩，与 Shell 模式同一份。 */
  overlay?: ReactNode
}

/**
 * Web UI 模式的主区：替换 Shell 模式的 BSP 分屏。只读取同一份快照，不调用任何 Herdr 接口；
 * Claude pane 显示对话视图，其他 pane 显示只读画面加一行命令输入。
 */
export function ConsoleSurface({ hostID, client, panes, paneID, onSelectPane, compact, connected, connectionEpoch, loading, showPanePicker, onOpenInShell, onShell, onNewSession, overlay }: ConsoleSurfaceProps) {
  const theme = useConsoleScreenTheme()
  const pane = panes.find((item) => item.pane_id === paneID)
  let content: ReactNode
  if (loading) {
    content = <div className="console-empty" role="status"><p>加载 Herdr 会话…</p></div>
  } else if (!pane) {
    content = <div className="console-empty">
      <MessageSquarePlus size={28} aria-hidden="true"/>
      <h2>这里还没有会话</h2>
      <p>在这台主机上新建一个 Claude 会话；需要管理终端时切换到 Shell 模式。</p>
      <div className="console-empty-actions">
        <Button className="console-button console-button-primary" onClick={onNewSession}><MessageSquarePlus size={15} aria-hidden="true"/>新建 Claude 会话</Button>
        <Button className="console-button" onClick={onShell}><SquareTerminal size={15} aria-hidden="true"/>切换到 Shell 模式</Button>
      </div>
    </div>
  } else if (consoleShowsChat(pane)) {
    content = <ConsoleChatView key={pane.pane_id} variant="console" hostID={hostID} pane={pane} client={client} compact={compact} connected={connected} onSwitchToTerminal={() => onOpenInShell(pane.pane_id)}/>
  } else {
    content = <div className="console-screen" key={pane.pane_id}>
      <div className="console-screen-view"><ScreenView client={client} pane={pane} compact={compact} connected={connected} connectionEpoch={connectionEpoch} theme={theme}/></div>
      <ConsoleCommandInput hostID={hostID} paneID={pane.pane_id} connected={connected} onOpenInShell={() => onOpenInShell(pane.pane_id)}/>
    </div>
  }
  return <section className={`console-surface${compact ? ' console-surface-compact' : ''}`} aria-label="Web UI 工作区">
    {overlay}
    {showPanePicker && panes.length > 1 && <nav className="console-pane-picker" aria-label="选择终端">
      {panes.map((item) => <button type="button" key={item.pane_id} className="console-pane-chip" aria-pressed={item.pane_id === paneID} data-tooltip={`${paneDisplayName(item)} · ${agentStatusLabel(item.agent_status || 'unknown')}`} onClick={() => onSelectPane(item.pane_id)}>
        <StatusDot status={item.agent_status || 'unknown'}/><span>{paneDisplayName(item)}</span>
      </button>)}
    </nav>}
    {pane && <FirstMessageBanner hostID={hostID} paneID={pane.pane_id}/>}
    <div className="console-main">{content}</div>
  </section>
}
