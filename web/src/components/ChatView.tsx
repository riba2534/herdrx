import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type WheelEvent } from 'react'
import { Clock3, MessageSquare, RefreshCw, SquareTerminal, TriangleAlert } from 'lucide-react'
import { ChatMediaComposer } from './media/ChatMediaComposer'
import { VoiceInput } from './media/VoiceInput'
import { Button, StatusDot } from './ui'
import { api } from '../lib/api'
import { readComposerDraft, readComposerSend, writeComposerDraft } from '../lib/composerDrafts'
import { voiceTransport } from '../lib/voiceClient'
import type { VoiceInputState } from '../lib/chatMediaTypes'
import { formatChatTime, paneAgentRejectsReadSource, shortSessionID, StructuredChatSession, type StructuredChatSnapshot } from '../lib/structuredChat'
import type { ChatReason, ChatSource } from '../lib/structuredChatTypes'
import { buildChatTurns, type TurnActivity } from '../lib/chat/workRuns'
import { ArrivalTracker } from '../lib/chat/arrivals'
import { collectUnreadKeys } from '../lib/chat/unreadCount'
import { insideScrollableAbove, isUpwardWheelIntent, shouldRepinOnWheel } from '../lib/chat/autoFollow'
import { ChatTurn } from './chat/ChatTurn'
import { ChatChips } from './chat/ChatChips'
import { ConsoleStatusBar } from './console/ConsoleStatusBar'
import { InteractionPanel } from './console/InteractionPanel'
import { useConsoleControls } from '../lib/console/useConsoleControls'
import { useConsoleLive } from '../lib/console/useConsoleLive'
import { agentStatusLabel, paneDisplayName } from '../lib/labels'
import type { WorkbenchClient } from '../lib/workbench'
import type { Pane } from '../types'
import './ChatView.css'

export type ChatViewProps = {
  hostID: string
  pane: Pane
  client: WorkbenchClient
  compact: boolean
  connected: boolean
  /**
   * `pane`（默认）：Shell 模式里覆盖在 pane 上的对话视图，宽度跟随 pane。
   * `console`：Web UI 模式的对话控制台，正文列居中、限宽。
   */
  variant?: 'pane' | 'console'
  /** pane 变体里是「切回终端视图」；console 变体里是「在 Shell 中打开这个 pane」。 */
  onSwitchToTerminal: () => void
  /**
   * 终端视图的整页拖放入口（`inject=true`，会把路径打进终端）。对话视图**不再使用**它：
   * 图片一律走 `stageImage`（`inject=false`，只落盘、返回远端路径，由发送时合成）。
   * 保留该 prop 只为调用方无需区分视图模式。
   */
  onPasteImages?: (files: File[]) => void
  onFocus?: () => void
}

/**
 * Chat 视图。
 *
 * 数据源是 Agent 自己写下的会话日志（经 owner 认证的 HTTP 端点增量读取），**完全不读
 * `pane.read` 终端文本**——屏幕文本没有 role，冒充不了逐轮问答。发送经 `api.panePrompt`
 * 读屏防护后单次输入到当前终端，不新建、不恢复任何 Agent 会话。Herdr 集成上报了该 pane 的会话时直接打开它，否则用户必须先显式选择"此终端的会话记录"，
 * 页面才会显示记录；没有权威数据时只显示可执行的降级说明，绝不编造助手回答。
 *
 * 对话流按轮次排：用户提问 → 工作组（思考、工具调用、中间文字）→ 最终回复；Agent 正在工作时
 * 最后一段动作保持展开并计时。新到达的助手正文按词淡入，历史记录不播。
 *
 * console 变体在输入区上方加运行状态栏（读屏得到的 spinner、模式、实时画面），Claude 提问、
 * 计划批准等交互用卡片顶替输入框；卡片收成条时输入框回来，草稿一直保留。
 *
 * 输入区是 `ChatMediaComposer`（契约 §8.4）：拖拽 / 粘贴 / 选择图片只落占位并走
 * `inject=false` 的 stage-only 上传，正文与图片引用在发送时合成**一次**提交（仅图片也能发）；
 * 语音输入只把**用户自己**的最终转写追加进草稿，没有自动发送，语音模型的回答只在这里
 * 单独展示，绝不写草稿或终端。
 */
export function ChatView({ hostID, pane, client, compact, connected, variant = 'pane', onSwitchToTerminal, onFocus }: ChatViewProps) {
  const paneID = pane.pane_id
  const session = useMemo(() => new StructuredChatSession({ hostID, paneID }), [hostID, paneID])
  const snapshot: StructuredChatSnapshot = useSyncExternalStore(session.subscribe, session.getSnapshot)
  const [docVisible, setDocVisible] = useState(() => typeof document === 'undefined' || document.visibilityState !== 'hidden')
  const [atLatest, setAtLatest] = useState(true)
  const [unread, setUnread] = useState<ReadonlySet<string>>(() => new Set())
  const [voiceReply, setVoiceReply] = useState('')
  const voiceFinalRef = useRef(false)
  const logRef = useRef<HTMLDivElement>(null)
  const followingRef = useRef(true)
  const logSizeRef = useRef({ height: 0, viewport: 0, top: 0 })
  const touchYRef = useRef<number | null>(null)
  const arrivals = useRef(new ArrivalTracker()).current
  const unreadSessionRef = useRef<string | undefined>(undefined)
  const consoleVariant = variant === 'console'
  // 交互卡片展开时顶替输入框；输入框只是隐藏，草稿、图片和语音状态都不丢。
  const [cardActive, setCardActive] = useState(false)
  const live = useConsoleLive({ hostID, paneID, client, enabled: consoleVariant && docVisible && pane.agent === 'claude' })
  const controls = useConsoleControls({ client, paneID, mode: live.mode })

  const setFollowing = useCallback((following: boolean) => {
    followingRef.current = following
    if (logRef.current) logRef.current.dataset.following = String(following)
  }, [])

  const updateLatestIndicator = useCallback((gap: number) => {
    setAtLatest((previous) => gap <= (previous ? 128 : 64))
  }, [])

  const syncLatest = useCallback(() => {
    const log = logRef.current
    if (!log) return
    const bottom = Math.max(0, log.scrollHeight - log.clientHeight)
    if (followingRef.current && Math.abs(log.scrollTop - bottom) > 1) log.scrollTop = bottom
    // Record our own write synchronously, before its queued scroll event arrives.
    const gap = Math.max(0, bottom - log.scrollTop)
    logSizeRef.current = { height: log.scrollHeight, viewport: log.clientHeight, top: log.scrollTop }
    updateLatestIndicator(gap)
  }, [updateLatestIndicator])

  const jumpToLatest = useCallback(() => {
    setFollowing(true)
    syncLatest()
  }, [setFollowing, syncLatest])

  const onLogScroll = useCallback(() => {
    const log = logRef.current
    if (!log) return
    // Content/input resizing is not a request to stop following the latest turn.
    const size = logSizeRef.current
    const bottom = Math.max(0, log.scrollHeight - log.clientHeight)
    // A shrinking document can clamp scrollTop; movement above that clamp is user navigation.
    if (log.scrollTop < Math.min(size.top, bottom) - 1) setFollowing(false)
    if (size.height !== log.scrollHeight || size.viewport !== log.clientHeight) {
      syncLatest()
      return
    }
    const gap = Math.max(0, bottom - log.scrollTop)
    if (gap <= 2 && log.scrollTop > size.top + 1) setFollowing(true)
    logSizeRef.current = { ...size, top: log.scrollTop }
    updateLatestIndicator(gap)
  }, [setFollowing, syncLatest, updateLatestIndicator])

  // 停止跟随只看用户意图：垂直为主的上滚才算，落在还能上滚的内层滚动区（长输出、diff）里不算；
  // 已经贴底时继续往下滚恢复跟随（这时不会再有 scroll 事件）。
  const onLogWheel = useCallback((event: WheelEvent<HTMLDivElement>) => {
    const log = logRef.current
    if (!log) return
    if (isUpwardWheelIntent(event)) {
      if (followingRef.current && !insideScrollableAbove(event.target, log)) setFollowing(false)
      return
    }
    if (followingRef.current) return
    const distanceFromBottom = Math.max(0, log.scrollHeight - log.clientHeight - log.scrollTop)
    if (shouldRepinOnWheel({ deltaX: event.deltaX, deltaY: event.deltaY, distanceFromBottom })) jumpToLatest()
  }, [jumpToLatest, setFollowing])

  /**
   * 图片 stage-only 上传：`inject=false` 只把图片落到远端并返回路径，**绝不**向终端打字。
   * 路径只在用户点发送时由媒体层合成进同一次发送。
   */
  const stageImage = useCallback(
    (host: string, targetPane: string, file: File) =>
      api.pasteImage(host, targetPane, file, false).then((result) => ({ path: result.path })),
    [],
  )

  /**
   * 语音助手（**非终端 Agent**）的回答只在这里单独渲染：不确定的增量按块累积，
   * 最终帧以服务端下发的完整文本为准；绝不写入草稿，也绝不进终端。
   */
  const onVoiceAssistantText = useCallback((text: string, final: boolean) => {
    const restart = final || voiceFinalRef.current
    voiceFinalRef.current = final
    setVoiceReply((previous) => (restart ? text : previous + text))
  }, [])

  // 新一轮语音会话开始：清掉上一轮的回答，避免两轮拼接。
  const onVoiceStatus = useCallback((state: VoiceInputState) => {
    if (state !== 'requesting') return
    voiceFinalRef.current = false
    setVoiceReply('')
  }, [])

  const { state, error, candidates } = snapshot
  const status = pane.agent_status || 'unknown'
  // 最新一轮还在进行时不折叠：此时最后一段动作还在跑，最后一条正文也还不是最终回答。
  // 记录还没读回来时不先画一个「正在工作」的空组。
  const activity: TurnActivity = (status === 'working' || status === 'blocked') && state.status !== 'loading' ? status : null
  const turns = useMemo(() => buildChatTurns(state.messages, activity), [state.messages, activity])
  // 先记下哪些助手记录是加载之后新到的（只有它们播淡入），再渲染；同一份输入重复观察不改变结果。
  arrivals.observe(state.session, state.status, state.messages, performance.now())

  // pane 变成已识别的 claude / codex 之后，服务端会拒绝 `source=dsh`（snapshot 每次现取）。
  // 这里在开始取数**之前**先放弃显式读取源：作废在途请求并清空选择 / 游标 / 候选，不继续发
  // 注定被拒的请求。草稿与发送目标都不受影响（它们不属于会话状态）。
  useEffect(() => {
    if (!paneAgentRejectsReadSource(pane.agent)) return
    session.setReadSource(undefined)
  }, [session, pane.agent, state.source])

  useEffect(() => {
    session.start()
    return () => session.stop()
  }, [session])

  useEffect(() => {
    if (typeof document === 'undefined') return
    const update = () => setDocVisible(document.visibilityState !== 'hidden')
    update()
    document.addEventListener('visibilitychange', update)
    return () => document.removeEventListener('visibilitychange', update)
  }, [])

  // 断线或页面隐藏时暂停轮询，恢复后立刻补一次增量；不重读全量。
  useEffect(() => { session.setActive(connected && docVisible) }, [session, connected, docVisible])

  useLayoutEffect(() => { jumpToLatest() }, [session, state.session, jumpToLatest])

  // 离开底部期间追加到尾部的回复计入「N 条新消息」；回到底部或换会话即清零。
  useLayoutEffect(() => {
    if (unreadSessionRef.current !== state.session) {
      unreadSessionRef.current = state.session
      setUnread(new Set())
      return
    }
    const keys = collectUnreadKeys({ messages: state.messages, appended: arrivals.appendedIn(state.messages), nearBottom: followingRef.current })
    if (keys.length) setUnread((current) => new Set([...current, ...keys]))
  }, [arrivals, state.session, state.messages])

  useEffect(() => { if (atLatest) setUnread((current) => (current.size ? new Set() : current)) }, [atLatest])

  // Include pane-status and local UI commits, not only transcript polling.
  useLayoutEffect(() => { syncLatest() })

  useLayoutEffect(() => {
    const log = logRef.current
    if (!log || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(syncLatest)
    observer.observe(log)
    const observed = new Set<Element>()
    // 正文列包着全部轮次，它的尺寸变化（新记录、展开 / 收起一组）就足以保持贴底；
    // 只在记录区的直接子节点增减时重新登记，不跟着正文里的每一次 DOM 变化跑。
    const observeChildren = () => {
      const current = new Set<Element>(log.children)
      for (const child of observed) {
        if (!current.has(child)) { observer.unobserve(child); observed.delete(child) }
      }
      for (const child of current) {
        if (!observed.has(child)) { observer.observe(child); observed.add(child) }
      }
      syncLatest()
    }
    observeChildren()
    const mutations = new MutationObserver(observeChildren)
    mutations.observe(log, { childList: true })
    return () => { mutations.disconnect(); observer.disconnect() }
  }, [syncLatest])

  const name = paneDisplayName(pane)
  const bound = state.session !== undefined
  const verified = state.binding === 'verified'
  // 读取源只来自用户在本 pane 的显式选择，绝不从窗口标题、进程名或候选文件名推断。
  const dshSelected = state.source === 'dsh'
  // 只有"没识别出程序"或"识别出的程序不支持"时才提供 DSH 入口；已知的 claude / codex 不给。
  const canOfferDsh = !bound && !dshSelected && (state.reason === 'no_agent' || state.reason === 'unsupported_agent')
  // 显式读取源只有一个入口动作，文案与 tooltip 在一处定义，避免两处写法漂移。
  const dshOffer = (
    <Button className="button-secondary chat-dsh-source" aria-label="读取 DSH 会话记录" data-tooltip="按 DSH 的会话目录（默认 ~/.dsh/sessions）读取记录；只用于读取，不改变终端里运行的程序，也不改变输入目标" onClick={() => session.setReadSource('dsh')}>读取 DSH 会话记录</Button>
  )
  const terminalLabel = consoleVariant ? '在 Shell 中打开' : '显示终端'
  const terminalHint = consoleVariant ? '切到 Shell 模式查看这个终端（任务不中断）' : '切换回终端视图（终端不中断）'
  const fallbackLabel = consoleVariant ? '在 Shell 中打开' : '切回终端'

  return <section className="chat-view" data-variant={variant} role="region" aria-label="对话视图" onPointerDown={onFocus}>
    <header className="chat-head">
      <span className="chat-head-title">
        <StatusDot status={status}/><strong>{name}</strong><small>{agentStatusLabel(status)}</small>
      </span>
      <span className="chat-head-actions">
        {dshSelected && <span className="chat-head-source" data-tooltip="当前读取源：DSH 会话记录（只读，不改变终端里的程序）">DSH</span>}
        {verified && <span className="chat-head-source chat-head-verified" data-tooltip="Herdr 的 Agent 集成上报了这个终端正在写的会话，已自动打开；可点「重选」改看其它会话">已确认</span>}
        {bound && <span className="chat-head-session" data-tooltip="当前读取的会话记录"><code>{shortSessionID(state.sessionID || '')}</code>{state.agent && <small>{state.agent}</small>}</span>}
        {bound && <Button className="tool-button chat-reselect" aria-label="重新选择会话记录" data-tooltip="回到候选列表，重新选择读取哪一个会话记录" onClick={() => session.reselect()}>重选</Button>}
        {dshSelected && <Button className="tool-button chat-source-reset" aria-label="恢复默认读取源" data-tooltip="停止按 DSH 读取，改回按当前 Agent 写下的记录读取（会清空当前选择）" onClick={() => session.clearReadSource()}>默认源</Button>}
        <Button className="tool-button" aria-label="刷新会话记录" data-tooltip="立即重新读取会话记录" onClick={() => session.refresh()}><RefreshCw size={14}/></Button>
        <Button className="tool-button" aria-label={terminalLabel} data-tooltip={terminalHint} onClick={onSwitchToTerminal}><SquareTerminal size={14}/></Button>
      </span>
    </header>
    {(!consoleVariant || !bound) && <p className="chat-notice" role="note">
      <MessageSquare size={13} aria-hidden="true"/>
      <span>这里的回答来自 Agent 写下的会话记录；输入会发送到<strong>当前终端</strong>（不会新建或恢复会话）。{dshSelected ? '当前按你选择的 DSH 读取源读取，只用于查看，不改变终端里运行的程序。' : ''}{verified ? '会话由 Herdr 上报自动匹配，只用于读取。' : bound ? '所选会话只用于读取，输入不会写入它。' : '请选择此终端的会话记录后再查看逐轮对话。'}</span>
    </p>}

    <div className="chat-log-wrap">
      <div className="chat-log" ref={logRef} tabIndex={0} role="log" aria-label="对话记录" onScroll={onLogScroll}
        onPointerDownCapture={(event) => { if (event.target === event.currentTarget || event.button === 1) setFollowing(false) }}
        onWheelCapture={onLogWheel}
        onTouchStart={(event) => { touchYRef.current = event.touches[0]?.clientY ?? null }}
        onTouchMove={(event) => {
          const y = event.touches[0]?.clientY
          if (y !== undefined && touchYRef.current !== null && y > touchYRef.current) setFollowing(false)
          touchYRef.current = y ?? null
        }}
        onKeyDown={(event) => { if (['ArrowUp', 'PageUp', 'Home'].includes(event.key) || (event.key === ' ' && event.shiftKey)) setFollowing(false) }}
      >
        <div className="chat-column">
          {state.status === 'loading' && !bound && !candidates.length && <div className="chat-state" role="status"><Clock3 size={22} aria-hidden="true"/><strong>正在读取会话记录</strong><span>从该终端所在主机读取 Agent 自己写下的会话记录。</span></div>}
          {state.status === 'unavailable' && <div className="chat-state chat-state-error" role="alert" aria-label="结构化记录不可用">
            <TriangleAlert size={22} aria-hidden="true"/><strong>暂不支持结构化会话记录</strong>
            <span>{reasonText(state.reason, pane.agent, state.source)}</span>
            <span className="chat-state-actions">
              {canOfferDsh && dshOffer}
              <Button className="button-secondary" onClick={onSwitchToTerminal}>{fallbackLabel}</Button>
            </span>
          </div>}
          {error && <div className="chat-state chat-state-error" role="alert" aria-label="读取会话记录失败">
            <TriangleAlert size={22} aria-hidden="true"/><strong>读取会话记录失败</strong><span>{error}</span>
            <span className="chat-state-actions"><Button className="button-secondary" onClick={() => session.refresh()}>重试</Button><Button className="button-secondary" onClick={onSwitchToTerminal}>{fallbackLabel}</Button></span>
          </div>}
          {!error && state.reason === 'session_unavailable' && <div className="chat-state" role="alert">
            <TriangleAlert size={22} aria-hidden="true"/><strong>该会话记录已失效</strong><span>{reasonText('session_unavailable', pane.agent)}</span>
          </div>}
          {!error && !bound && state.status !== 'unavailable' && state.status !== 'loading' && <>
            <section className="chat-candidates" aria-label="会话记录候选">
              <h3>{dshSelected ? '选择 DSH 会话记录' : '选择此终端的会话记录'}</h3>
              <p className="chat-candidates-hint">{dshSelected
                ? '列表只包含 DSH 会话目录（默认 ~/.dsh/sessions）下、header 里 cwd 与该终端一致的会话文件。'
                : '列表只包含该终端目录下由这个 Agent 写下的会话文件，不含其它会话内容。'}</p>
              {candidates.length
                ? <ul className="chat-candidate-list">
                  {candidates.map((candidate) => <li key={candidate.id}>
                    <button type="button" className="chat-candidate" onClick={() => session.select(candidate.id)}>
                      <span className="chat-candidate-agent">{candidate.agent}</span>
                      <code className="chat-candidate-id">{shortSessionID(candidate.session_id)}</code>
                      <small className="chat-candidate-time">{formatChatTime(candidate.updated_at) || '时间未知'}</small>
                    </button>
                  </li>)}
                </ul>
                : <p className="chat-candidates-empty">{reasonText(state.reason || 'no_session_candidates', pane.agent, state.source)}</p>}
              {/* 没有候选也可能只是"这个程序我们没识别出来"：给一个显式入口，但不自动读取任何东西。 */}
              {canOfferDsh && <div className="chat-state-actions">{dshOffer}</div>}
            </section>
          </>}
          {!error && bound && <>
            {state.previousCursor && <div className="chat-earlier"><Button className="button-secondary chat-load-earlier" onClick={() => session.loadEarlier()}>加载更早的记录</Button></div>}
            {state.skipped > 0 && <p className="chat-skipped" role="note">有 {state.skipped} 条记录格式无法识别，可能未完整显示。</p>}
            {state.truncated > 0 && <p className="chat-truncated" role="note">这一行记录过大，部分内容未显示，可在终端查看。</p>}
            {/* 受限读取超限：说清楚"读不下"，而不是让下面的空态冒充"这个会话没有记录"。 */}
            {state.reason === 'read_limit_exceeded' && <p className="chat-limit" role="alert">{reasonText('read_limit_exceeded', pane.agent, state.source)}</p>}
            {turns.map((turn) => <ChatTurn key={turn.key} turn={turn} fade={arrivals.arrivedAt}/>)}
            {state.status === 'empty' && !state.previousCursor && state.reason !== 'read_limit_exceeded' && !activity && <div className="chat-state" role="status"><MessageSquare size={22} aria-hidden="true"/><strong>这个会话还没有记录</strong><span>Agent 写盘后会自动出现；也可以直接在下方输入。</span></div>}
          </>}
        </div>
      </div>
      <ChatChips visible={!atLatest && bound} unread={unread.size} onJump={jumpToLatest}/>
    </div>

    {!connected && <p className="chat-disconnected" role="alert">主机未连接，已暂停读取会话记录，也不能发送；重新连接后会自动继续。</p>}
    {voiceReply !== '' && <div className="voice-assistant-note" role="note">
      <span className="voice-assistant-note-head">语音助手（非终端 Agent）</span>
      <span className="voice-assistant-note-text">{voiceReply}</span>
      <Button className="button-secondary" onClick={() => setVoiceReply('')}>关闭</Button>
    </div>}
    <div className="chat-compose" data-card={cardActive || undefined}>
      <div className="chat-compose-column">
        {consoleVariant && <div className="chat-console-controls">
          <ConsoleStatusBar live={live} working={status === 'working'} onInterrupt={controls.interrupt} onCycleMode={controls.cycleMode}/>
          <InteractionPanel hostID={hostID} pane={pane} records={state.messages} live={live} onActiveChange={setCardActive} onOpenShell={onSwitchToTerminal}/>
        </div>}
        <div className="chat-composer-slot" hidden={cardActive}>
          <ChatMediaComposer
            variant="chat"
            hostID={hostID}
            paneID={paneID}
            visible
            compact={compact}
            sendDisabled={!connected}
            placeholder={!connected ? '主机未连接，暂不能发送' : '输入消息，Enter 发送到当前终端'}
            onDirectInput={() => {}}
            onLocalInput={() => {}}
            stageImage={stageImage}
            readSend={readComposerSend}
            voice={
              <VoiceInput
                hostID={hostID}
                paneID={paneID}
                visible
                compact={compact}
                transport={voiceTransport}
                writeDraft={writeComposerDraft}
                readDraft={readComposerDraft}
                onAssistantText={onVoiceAssistantText}
                onStatus={onVoiceStatus}
              />
            }
          />
        </div>
      </div>
    </div>
  </section>
}

/**
 * 降级文案：说明现状并给出用户下一步能做的事，不推荐未实现的命令。
 *
 * 措辞纪律：只描述**已经观察到的事实**。agent 为空只说明"没识别出程序"，
 * **不能**据此断言它是普通 Shell、也不能断言它没有会话记录。
 */
function reasonText(reason: ChatReason | undefined, agent?: string, source?: ChatSource) {
  switch (reason) {
    case 'unsupported_agent':
      return agent ? `该终端运行的 ${agent} 暂无结构化会话记录，可切回终端查看完整界面。` : '该终端运行的 Agent 暂无结构化会话记录，可切回终端查看完整界面。'
    case 'no_agent':
      return '未识别出这个终端里运行的程序，因此不知道它有没有会话记录；输入仍会发送到当前终端。'
    case 'unsupported_transport':
      return '当前接入方式或远端环境暂不支持受限读取（例如缺少所需工具），已保留完整终端视图。'
    case 'cwd_unavailable':
    case 'log_root_unavailable':
      return source === 'dsh'
        ? '暂时读不到 DSH 的会话目录（默认 ~/.dsh/sessions），可稍后重试或切回终端查看完整界面。'
        : '暂时无法定位这个终端的会话记录，已开始自动重试。'
    case 'session_unavailable':
      return '该会话记录已失效，请在上方重新选择。'
    case 'no_session_candidates':
      return source === 'dsh'
        ? '在 DSH 的会话目录（默认 ~/.dsh/sessions）下没有找到与这个终端目录匹配的会话记录，可切回终端查看完整界面。'
        : '尚未找到这个终端的会话记录（Agent 刚启动时可能还没写盘），可切回终端查看完整界面。'
    case 'read_denied':
      return '读取会话记录被拒绝（路径越界或文件类型不被允许），可切回终端查看完整界面。'
    case 'unrecognized_format':
      return '会话日志格式无法识别，暂时不能显示逐轮对话，可切回终端查看完整界面。'
    case 'read_limit_exceeded':
      return '这个会话记录超出受限读取上限（压缩文件或解压结果过大），没有读取正文；可切回终端查看完整界面。'
    default:
      return '暂时无法显示结构化会话记录，可稍后重试或切回终端查看完整界面。'
  }
}
