import { useState } from 'react'
import { ChevronDown, ChevronUp, ClipboardList, MessageCircleQuestion } from 'lucide-react'
import { askAnswers, planOutcome } from '../../lib/chat/interactionResults'
import type { ToolEntry } from '../../lib/chat/workRuns'
import { ChatMarkdown } from './ChatMarkdown'

// 需要用户作答的工具在对话流里单独成卡，不收进工作记录。这里只读会话记录展示问题、计划
// 和结果；作答入口由交互面板提供（读屏校验后按键），卡片本身不发任何输入。

const ASK_STATE = { pending: '等待回答', answered: '已回答', cancelled: '已取消' }
const PLAN_STATE = { pending: '等待批准', approved: '已批准', rejected: '未批准' }

function CardHead({ icon: Icon, title, state }: { icon: typeof ClipboardList; title: string; state: string }) {
  return <header className="chat-card-head">
    <Icon size={15} aria-hidden="true"/>
    <span className="chat-card-title">{title}</span>
    <span className="chat-card-state">{state}</span>
  </header>
}

function AskCard({ entry }: { entry: ToolEntry }) {
  const questions = entry.descriptor.kind === 'question' ? entry.descriptor.questions : []
  const { state, answers } = askAnswers(questions, entry.result)
  return <section className="chat-card chat-ask" data-state={state} aria-label="Agent 的提问">
    <CardHead icon={MessageCircleQuestion} title="提问" state={ASK_STATE[state]}/>
    <ol className="chat-ask-list">
      {questions.map((question, index) => {
        const answer = answers.get(question.question)
        return <li key={index} className="chat-ask-item">
          {question.header && <span className="chat-ask-header">{question.header}</span>}
          <p className="chat-ask-question">{question.question}</p>
          {answer !== undefined
            ? <p className="chat-ask-answer"><span aria-hidden="true">→</span> {answer || '（空）'}</p>
            : state === 'pending' && question.options.length > 0 && <ul className="chat-ask-options">
              {question.options.map((option) => <li key={option.label}><span>{option.label}</span>{option.description && <small>{option.description}</small>}</li>)}
            </ul>}
        </li>
      })}
    </ol>
  </section>
}

function PlanCard({ entry }: { entry: ToolEntry }) {
  const [open, setOpen] = useState(false)
  const plan = entry.descriptor.kind === 'plan' ? entry.descriptor.plan : ''
  const { state, feedback } = planOutcome(entry.result)
  return <section className="chat-card chat-plan" data-state={state} aria-label="Agent 的计划">
    <CardHead icon={ClipboardList} title="计划" state={PLAN_STATE[state]}/>
    {plan && <div className="chat-plan-body" data-open={open || undefined}><ChatMarkdown text={plan}/></div>}
    {plan && <button type="button" className="chat-pill chat-plan-toggle" aria-expanded={open} onClick={() => setOpen((value) => !value)}>
      {open ? <ChevronUp size={13} aria-hidden="true"/> : <ChevronDown size={13} aria-hidden="true"/>}{open ? '收起计划' : '展开计划'}
    </button>}
    {feedback && <p className="chat-plan-feedback"><strong>修改意见：</strong>{feedback}</p>}
  </section>
}

export function InteractionCard({ entry }: { entry: ToolEntry }) {
  return entry.descriptor.kind === 'plan' ? <PlanCard entry={entry}/> : <AskCard entry={entry}/>
}
