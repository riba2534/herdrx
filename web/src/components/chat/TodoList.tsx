// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) apps/desktop/src/renderer/components/chat/TodoListCard.tsx@19dad9c225
//
// TodoWrite 的待办清单：已完成置灰划掉，进行中高亮，待开始正常前景色。

import { Circle, CircleCheck, CircleDot } from 'lucide-react'
import type { TodoItem } from '../../lib/chat/toolDescriptor'

export function todoProgress(todos: readonly TodoItem[]) {
  return `${todos.filter((todo) => todo.status === 'completed').length}/${todos.length} 已完成`
}

const ICONS = { completed: CircleCheck, in_progress: CircleDot, pending: Circle }
const STATUS = { completed: '已完成', in_progress: '进行中', pending: '待开始' }

export function TodoList({ todos }: { todos: readonly TodoItem[] }) {
  if (!todos.length) return <p className="chat-detail-empty">清单为空。</p>
  return <ol className="chat-todo" aria-label="待办清单">
    {todos.map((todo, index) => {
      const Icon = ICONS[todo.status]
      return <li key={index} className="chat-todo-item" data-status={todo.status}>
        <Icon size={14} aria-label={STATUS[todo.status]}/>
        <span>{todo.status === 'in_progress' && todo.activeForm ? todo.activeForm : todo.content}</span>
      </li>
    })}
  </ol>
}
