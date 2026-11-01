import { memo } from 'react'
// 一轮对话：用户提问是右侧白卡气泡（纯文本，不当 Markdown 解析）；助手的最终回复不加气泡，
// 直接是正文；中间过程收进工作组；需要作答的工具单独成卡；系统提示居中弱化。

import { recordText } from '../../lib/structuredChatMarkdown'
import type { ChatRecord } from '../../lib/structuredChatTypes'
import type { AnswerBlock, ChatTurnView, TurnBlock } from '../../lib/chat/workRuns'
import { ChatCopyButton } from './ChatCopyButton'
import { ChatMarkdown } from './ChatMarkdown'
import { ElisionNote } from './ElisionNote'
import { InteractionCard } from './InteractionCard'
import { WorkGroup } from './WorkGroup'

/** 一条记录到达页面的时刻；只有加载之后新到的助手正文才有，用来播放淡入。 */
type FadeLookup = (recordID: string) => number | undefined

function UserMessage({ record }: { record: ChatRecord }) {
  const text = recordText(record)
  return <article className="chat-message chat-message-user" data-role="user" data-record-id={record.id}>
    {record.queued && <small className="chat-queued">工作中插入</small>}
    <p className="chat-bubble">{text}</p>
    <div className="chat-message-actions"><ChatCopyButton label="复制这条用户消息" text={text} className="chat-message-copy"/></div>
  </article>
}

function Answer({ block, fade }: { block: AnswerBlock; fade: FadeLookup }) {
  const text = block.entries.map((entry) => entry.text).join('\n\n')
  return <article className="chat-message chat-message-assistant" data-role="assistant" data-record-id={block.entries[0].recordID}>
    {block.entries.map((entry) => <div className="chat-assistant-text" key={entry.key} data-record-id={entry.recordID}>
      <ChatMarkdown text={entry.text} fadeFrom={fade(entry.recordID)}/>
      <ElisionNote text={entry.elided ?? entry.text}/>
    </div>)}
    <div className="chat-message-actions"><ChatCopyButton label="复制这条助手消息" text={text} className="chat-message-copy"/></div>
  </article>
}

function Block({ block, fade }: { block: TurnBlock; fade: FadeLookup }) {
  switch (block.kind) {
    case 'work':
      return <WorkGroup block={block}/>
    case 'answer':
      return <Answer block={block} fade={fade}/>
    case 'interaction':
      return <InteractionCard entry={block.entry}/>
    case 'queued':
      return <UserMessage record={block.record}/>
    case 'system':
      return <article className="chat-message chat-message-system" data-role="system" data-record-id={block.record.id}>
        <p>{recordText(block.record)}</p>
      </article>
  }
}

/** 轮次对象来自按记录记忆的分组结果：记录不变时同一个对象，轮询和滚动状态变化都不重绘它。 */
export const ChatTurn = memo(function ChatTurn({ turn, fade }: { turn: ChatTurnView; fade: FadeLookup }) {
  return <section className="chat-turn" data-live={turn.live || undefined}>
    {turn.user && <UserMessage record={turn.user}/>}
    {turn.blocks.map((block) => <Block key={block.key} block={block} fade={fade}/>)}
  </section>
})
