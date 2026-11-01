import { useState } from 'react'
import { ChevronDown, ChevronRight, ChevronUp, ClipboardList, MessageCircleQuestion } from 'lucide-react'
import { askAnswers, planOutcome } from '../../lib/chat/interactionResults'
import type { AskQuestion } from '../../lib/chat/toolDescriptor'
import type { ToolEntry } from '../../lib/chat/workRuns'
import { ChatMarkdown } from './ChatMarkdown'

// 需要用户作答的工具在对话流里单独成块，不收进工作记录。这里只读会话记录：
//   - 还没有结果时只占一行紧凑提示（作答入口是输入框位置的交互卡片，这里不重复展开），
//     点开可看只读的题目与选项 / 计划全文；
//   - 拿到结果后显示「问题 → 答案」或计划的批准 / 拒绝结果。
// 卡片本身不发任何输入。

const ASK_STATE = { answered: '已回答', cancelled: '已取消' }
const PLAN_STATE = { approved: '已批准', rejected: '未批准' }

function CardHead({ icon: Icon, title, state }: { icon: typeof ClipboardList; title: string; state: string }) {
  return <header className="chat-card-head">
    <Icon size={15} aria-hidden="true"/>
    <span className="chat-card-title">{title}</span>
    <span className="chat-card-state">{state}</span>
  </header>
}

/** 题目列表；有答案的题显示「→ 答案」，没有答案时按需列出只读的选项。 */
function QuestionList({ questions, answers, options }: { questions: readonly AskQuestion[]; answers?: ReadonlyMap<string, string>; options: boolean }) {
  return <ol className="chat-ask-list">
    {questions.map((question, index) => {
      const answer = answers?.get(question.question)
      return <li key={index} className="chat-ask-item">
        {question.header && <span className="chat-ask-header">{question.header}</span>}
        <p className="chat-ask-question">{question.question}</p>
        {answer !== undefined
          ? <p className="chat-ask-answer"><span aria-hidden="true">→</span> {answer || '（空）'}</p>
          : options && question.options.length > 0 && <ul className="chat-ask-options">
            {question.options.map((option) => <li key={option.label}><span>{option.label}</span>{option.description && <small>{option.description}</small>}</li>)}
          </ul>}
      </li>
    })}
  </ol>
}

/** 待处理：一行提示，点开看只读全文。 */
function PendingInteraction({ entry }: { entry: ToolEntry }) {
  const [open, setOpen] = useState(false)
  const { descriptor } = entry
  const plan = descriptor.kind === 'plan'
  const questions = descriptor.kind === 'question' ? descriptor.questions : []
  const Icon = plan ? ClipboardList : MessageCircleQuestion
  return <section className="chat-work chat-pending" data-state="pending" data-expanded={open || undefined} aria-label={plan ? '等待批准的计划' : '等待回答的提问'}>
    <button type="button" className="chat-work-head" aria-expanded={open} onClick={() => setOpen((value) => !value)}>
      <span className="chat-row-icon" aria-hidden="true"><Icon size={14}/></span>
      <span className="chat-work-title">{plan ? '计划等待批准' : `等待你回答 · ${questions.length} 个问题`}</span>
      <span className="chat-row-spacer"/>
      <span className="chat-row-chevron" aria-hidden="true"><ChevronRight size={14}/></span>
    </button>
    {open && <div className="chat-work-body">
      {descriptor.kind === 'plan'
        ? <div className="chat-work-prose"><ChatMarkdown text={descriptor.plan}/></div>
        : <div className="chat-work-prose"><QuestionList questions={questions} options/></div>}
    </div>}
  </section>
}

function AskCard({ entry }: { entry: ToolEntry }) {
  const questions = entry.descriptor.kind === 'question' ? entry.descriptor.questions : []
  const { state, answers } = askAnswers(questions, entry.result)
  return <section className="chat-card chat-ask" data-state={state} aria-label="Agent 的提问">
    <CardHead icon={MessageCircleQuestion} title="提问" state={state === 'cancelled' ? ASK_STATE.cancelled : ASK_STATE.answered}/>
    <QuestionList questions={questions} answers={answers} options={false}/>
  </section>
}

function PlanCard({ entry }: { entry: ToolEntry }) {
  const [open, setOpen] = useState(false)
  const plan = entry.descriptor.kind === 'plan' ? entry.descriptor.plan : ''
  const { state, feedback } = planOutcome(entry.result)
  return <section className="chat-card chat-plan" data-state={state} aria-label="Agent 的计划">
    <CardHead icon={ClipboardList} title="计划" state={state === 'rejected' ? PLAN_STATE.rejected : PLAN_STATE.approved}/>
    {plan && <div className="chat-plan-body" data-open={open || undefined}><ChatMarkdown text={plan}/></div>}
    {plan && <button type="button" className="chat-pill chat-plan-toggle" aria-expanded={open} onClick={() => setOpen((value) => !value)}>
      {open ? <ChevronUp size={13} aria-hidden="true"/> : <ChevronDown size={13} aria-hidden="true"/>}{open ? '收起计划' : '展开计划'}
    </button>}
    {feedback && <p className="chat-plan-feedback"><strong>修改意见：</strong>{feedback}</p>}
  </section>
}

export function InteractionCard({ entry }: { entry: ToolEntry }) {
  if (!entry.result) return <PendingInteraction entry={entry}/>
  return entry.descriptor.kind === 'plan' ? <PlanCard entry={entry}/> : <AskCard entry={entry}/>
}
