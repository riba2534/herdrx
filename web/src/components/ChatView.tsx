import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import { ChevronUp, Clock3, MessageSquare, RefreshCw, SquareTerminal, TriangleAlert } from 'lucide-react'
import { ChatMediaComposer } from './media/ChatMediaComposer'
import { VoiceInput } from './media/VoiceInput'
import { Button, StatusDot } from './ui'
import { api } from '../lib/api'
import { readComposerDraft, readComposerSend, runComposerSend, writeComposerDraft } from '../lib/composerDrafts'
import { voiceTransport } from '../lib/voiceClient'
import type { VoiceInputState } from '../lib/chatMediaTypes'
import { collectToolCalls, formatChatTime, groupChatTurns, shortSessionID, StructuredChatSession, type StructuredChatSnapshot } from '../lib/structuredChat'
import type { ChatReason } from '../lib/structuredChatTypes'
import { ChatMessage } from './chat/ChatMessage'
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
  submit: (paneID: string, text: string) => Promise<void>
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
 * Chat 视图：同一 pane 内的覆盖层。
 *
 * 数据源是 Agent 自己写下的会话日志（经 owner 认证的 HTTP 端点增量读取），**完全不读
 * `pane.read` 终端文本**——屏幕文本没有 role，冒充不了逐轮问答。发送仍走 `props.submit`
 * 单次输入到当前终端，不新建、不恢复任何 Agent 会话。用户必须先显式选择"此终端的会话记录"，
 * 页面才会显示记录；没有权威数据时只显示可执行的降级说明，绝不编造助手回答。
 *
 * 输入区是 `ChatMediaComposer`（契约 §8.4）：拖拽 / 粘贴 / 选择图片只落占位并走
 * `inject=false` 的 stage-only 上传，正文与图片引用在发送时合成**一次**提交（仅图片也能发）；
 * 语音输入只把**用户自己**的最终转写追加进草稿，没有自动发送，语音模型的回答只在这里
 * 单独展示，绝不写草稿或终端。
 */
export function ChatView({ hostID, pane, compact, connected, submit, onSwitchToTerminal, onFocus }: ChatViewProps) {
  const paneID = pane.pane_id
  const session = useMemo(() => new StructuredChatSession({ hostID, paneID }), [hostID, paneID])
  const snapshot: StructuredChatSnapshot = useSyncExternalStore(session.subscribe, session.getSnapshot)
  const [docVisible, setDocVisible] = useState(() => typeof document === 'undefined' || document.visibilityState !== 'hidden')
  const [atLatest, setAtLatest] = useState(true)
  const [voiceReply, setVoiceReply] = useState('')
  const voiceFinalRef = useRef(false)
  const logRef = useRef<HTMLDivElement>(null)

  /**
   * 图片 stage-only 上传：`inject=false` 只把图片落到远端并返回路径，**绝不**向终端打字。
   * 路径只在用户点发送时由媒体层合成进同一条 `submit`。
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
  const turns = useMemo(() => groupChatTurns(state.messages), [state.messages])
  const tools = useMemo(() => collectToolCalls(state.messages), [state.messages])

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

  useEffect(() => {
    if (!atLatest) return
    const log = logRef.current
    log?.scrollTo?.({ top: log.scrollHeight })
  }, [state.messages.length, turns.length, atLatest])

  const name = paneDisplayName(pane)
  const status = pane.agent_status || 'unknown'
  const bound = state.session !== undefined

  return <section className="chat-view" role="region" aria-label="对话视图" onPointerDown={onFocus}>
    <header className="chat-head">
      <span className="chat-head-title">
        <StatusDot status={status}/><strong>{name}</strong><small>{agentStatusLabel(status)}</small>
      </span>
      <span className="chat-head-actions">
        {bound && <span className="chat-head-session" title="当前读取的会话记录"><code>{shortSessionID(state.sessionID || '')}</code>{state.agent && <small>{state.agent}</small>}</span>}
        {bound && <Button className="tool-button chat-reselect" aria-label="重新选择会话记录" data-tooltip="回到候选列表，重新选择读取哪一个会话记录" onClick={() => session.reselect()}>重选</Button>}
        <Button className="tool-button" aria-label="刷新会话记录" data-tooltip="立即重新读取会话记录" onClick={() => session.refresh()}><RefreshCw size={14}/></Button>
        <Button className="tool-button" aria-label="显示终端" data-tooltip="切换回终端视图（终端不中断）" onClick={onSwitchToTerminal}><SquareTerminal size={14}/></Button>
      </span>
    </header>
    <p className="chat-notice" role="note">
      <MessageSquare size={13} aria-hidden="true"/>
      <span>这里的回答来自 Agent 写下的会话记录；输入会发送到<strong>当前终端</strong>（不会新建或恢复会话）。{bound ? '所选会话只用于读取，输入不会写入它。' : '请选择此终端的会话记录后再查看逐轮对话。'}</span>
    </p>

    <div className="chat-log-wrap">
      <div className="chat-log" ref={logRef} tabIndex={0} role="log" aria-label="对话记录" onScroll={() => {
        const log = logRef.current
        if (!log) return
        setAtLatest(log.scrollHeight - log.scrollTop - log.clientHeight < 24)
      }}>
        {state.status === 'loading' && !bound && !candidates.length && <div className="chat-state" role="status"><Clock3 size={22} aria-hidden="true"/><strong>正在读取会话记录</strong><span>从该终端所在主机读取 Agent 自己写下的会话记录。</span></div>}
        {state.status === 'unavailable' && <div className="chat-state chat-state-error" role="alert" aria-label="结构化记录不可用">
          <TriangleAlert size={22} aria-hidden="true"/><strong>暂不支持结构化会话记录</strong>
          <span>{reasonText(state.reason, pane.agent)}</span>
          <Button className="button-secondary" onClick={onSwitchToTerminal}>切回终端</Button>
        </div>}
        {error && <div className="chat-state chat-state-error" role="alert" aria-label="读取会话记录失败">
          <TriangleAlert size={22} aria-hidden="true"/><strong>读取会话记录失败</strong><span>{error}</span>
          <span className="chat-state-actions"><Button className="button-secondary" onClick={() => session.refresh()}>重试</Button><Button className="button-secondary" onClick={onSwitchToTerminal}>切回终端</Button></span>
        </div>}
        {!error && state.reason === 'session_unavailable' && <div className="chat-state" role="alert">
          <TriangleAlert size={22} aria-hidden="true"/><strong>该会话记录已失效</strong><span>{reasonText('session_unavailable', pane.agent)}</span>
        </div>}
        {!error && !bound && state.status !== 'unavailable' && state.status !== 'loading' && <>
          <section className="chat-candidates" aria-label="会话记录候选">
            <h3>选择此终端的会话记录</h3>
            <p className="chat-candidates-hint">列表只包含该终端目录下由这个 Agent 写下的会话文件，不含其它会话内容。</p>
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
              : <p className="chat-candidates-empty">{reasonText(state.reason || 'no_session_candidates', pane.agent)}</p>}
          </section>
        </>}
        {!error && bound && <>
          {state.previousCursor && <div className="chat-earlier"><Button className="button-secondary chat-load-earlier" onClick={() => session.loadEarlier()}>加载更早的记录</Button></div>}
          {state.skipped > 0 && <p className="chat-skipped" role="note">有 {state.skipped} 条记录格式无法识别，可能未完整显示。</p>}
          {pane.agent && status === 'working' && <p className="chat-waiting" role="status">Agent 正在工作（{agentStatusLabel(status)}），本轮写盘后会自动出现在下方。</p>}
          {turns.map((turn) => <article className="chat-turn" key={turn.key}>
            {turn.user && <div className="chat-turn-user"><ChatMessage record={turn.user} tools={tools}/></div>}
            {turn.responses.map((record) => <ChatMessage key={record.id} record={record} tools={tools}/>)}
          </article>)}
          {state.status === 'empty' && !state.previousCursor && <div className="chat-state" role="status"><MessageSquare size={22} aria-hidden="true"/><strong>这个会话还没有记录</strong><span>Agent 写盘后会自动出现；也可以直接在下方输入。</span></div>}
        </>}
      </div>
      {!atLatest && bound && <button type="button" className="chat-jump" onClick={() => { setAtLatest(true); const log = logRef.current; log?.scrollTo?.({ top: log.scrollHeight }) }}><ChevronUp size={12} aria-hidden="true"/>跳到最新</button>}
    </div>

    {!connected && <p className="chat-disconnected" role="alert">主机未连接，已暂停读取会话记录，也不能发送；重新连接后会自动继续。</p>}
    {voiceReply !== '' && <div className="voice-assistant-note" role="note">
      <span className="voice-assistant-note-head">语音助手（非终端 Agent）</span>
      <span className="voice-assistant-note-text">{voiceReply}</span>
      <Button className="button-secondary" onClick={() => setVoiceReply('')}>关闭</Button>
    </div>}
    <div className="chat-compose">
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
        submit={submit}
        stageImage={stageImage}
        send={runComposerSend}
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
  </section>
}

/** 降级文案：说明现状并给出用户下一步能做的事，不推荐未实现的命令。 */
function reasonText(reason: ChatReason | undefined, agent?: string) {
  switch (reason) {
    case 'unsupported_agent':
      return agent ? `该终端运行的 ${agent} 暂无结构化会话记录，可切回终端查看完整界面。` : '该终端运行的 Agent 暂无结构化会话记录，可切回终端查看完整界面。'
    case 'no_agent':
      return '这是普通 Shell，没有对话记录；发送内容会作为命令执行。'
    case 'unsupported_transport':
      return '当前接入方式或远端环境暂不支持受限读取（例如缺少所需工具），已保留完整终端视图。'
    case 'cwd_unavailable':
    case 'log_root_unavailable':
      return '暂时无法定位这个终端的会话记录，已开始自动重试。'
    case 'session_unavailable':
      return '该会话记录已失效，请在上方重新选择。'
    case 'no_session_candidates':
      return '尚未找到这个终端的会话记录（Agent 刚启动时可能还没写盘），可切回终端查看完整界面。'
    case 'read_denied':
      return '读取会话记录被拒绝（路径越界或文件类型不被允许），可切回终端查看完整界面。'
    case 'unrecognized_format':
      return '会话日志格式无法识别，暂时不能显示逐轮对话，可切回终端查看完整界面。'
    default:
      return '暂时无法显示结构化会话记录，可稍后重试或切回终端查看完整界面。'
  }
}
