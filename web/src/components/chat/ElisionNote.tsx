import { elisionNotice } from '../../lib/structuredChatMarkdown'

/** 服务端截断超大块后追加的说明（「…（已截断 N 字节）」），跟在被截断的那一块下面。 */
export function ElisionNote({ text }: { text?: string }) {
  const notice = text ? elisionNotice(text) : null
  return notice ? <p className="chat-elision" role="note">{notice}</p> : null
}
