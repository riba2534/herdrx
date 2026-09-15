/**
 * 手机端终端辅助键栏。
 *
 * 键位集合、顺序、字节序列与 orca 移动端 `TERMINAL_ACCESSORY_KEYS` 对齐：常用编辑键
 * 在前（Esc / Tab / Enter / Shift+Tab / Space / ⌫ / Del / 方向键），控制键在后，
 * 长按重复的键只有 ⌫、Del 和四个方向键。用户已经习惯 orca 这一套，两个客户端的
 * 拇指位置应当一致，避免来回切换时按错。
 *
 * `bytes` 是直接送进终端的原始字节，走与本地 xterm 同一条输入通道；不是按键名。
 */
export type TerminalAuxiliaryKey = {
  id: string
  label: string
  bytes: string
  /** 无障碍名称；按钮文字是符号，读屏需要中文说明。 */
  aria: string
  /** 长按是否连续发送；与 orca 的 repeatable 一致。 */
  repeatable?: boolean
}

export const TERMINAL_AUXILIARY_KEYS: TerminalAuxiliaryKey[] = [
  { id: 'escape', label: 'Esc', bytes: '\x1b', aria: 'Esc 键' },
  { id: 'tab', label: 'Tab', bytes: '\t', aria: 'Tab 键' },
  { id: 'enter', label: 'Enter', bytes: '\r', aria: '回车键' },
  // 终端程序把 ESC [ Z 识别为反向 Tab。
  { id: 'shiftTab', label: 'Shift+Tab', bytes: '\x1b[Z', aria: 'Shift 加 Tab 反向切换' },
  { id: 'space', label: 'Space', bytes: ' ', aria: '空格键' },
  { id: 'backspace', label: '⌫', bytes: '\x7f', aria: '退格键', repeatable: true },
  { id: 'delete', label: 'Del', bytes: '\x1b[3~', aria: '向前删除键', repeatable: true },
  { id: 'arrowUp', label: '↑', bytes: '\x1b[A', aria: '方向键上', repeatable: true },
  { id: 'arrowDown', label: '↓', bytes: '\x1b[B', aria: '方向键下', repeatable: true },
  { id: 'arrowLeft', label: '←', bytes: '\x1b[D', aria: '方向键左', repeatable: true },
  { id: 'arrowRight', label: '→', bytes: '\x1b[C', aria: '方向键右', repeatable: true },
  { id: 'ctrlC', label: 'Ctrl+C', bytes: '\x03', aria: '中断 Ctrl+C' },
  { id: 'ctrlD', label: 'Ctrl+D', bytes: '\x04', aria: '结束输入 Ctrl+D' },
  { id: 'ctrlL', label: 'Ctrl+L', bytes: '\x0c', aria: '清屏 Ctrl+L' },
  { id: 'ctrlZ', label: 'Ctrl+Z', bytes: '\x1a', aria: '挂起进程 Ctrl+Z' },
  { id: 'ctrlR', label: 'Ctrl+R', bytes: '\x12', aria: '反向搜索历史 Ctrl+R' },
  { id: 'ctrlA', label: 'Ctrl+A', bytes: '\x01', aria: '行首 Ctrl+A' },
  { id: 'ctrlE', label: 'Ctrl+E', bytes: '\x05', aria: '行尾 Ctrl+E' },
  { id: 'ctrlW', label: 'Ctrl+W', bytes: '\x17', aria: '向前删除一个词 Ctrl+W' },
  { id: 'ctrlU', label: 'Ctrl+U', bytes: '\x15', aria: '清空光标前内容 Ctrl+U' },
]

/**
 * 中文输入法下常常找不到这几个符号键，单独留在键栏末尾；orca 没有这几个键，
 * 属于 herdrx 既有补充，不参与上面的顺序对齐。
 */
export const TERMINAL_AUXILIARY_EXTRA_KEYS: TerminalAuxiliaryKey[] = [
  { id: 'hyphen', label: '-', bytes: '-', aria: '减号' },
  { id: 'slash', label: '/', bytes: '/', aria: '斜杠' },
  { id: 'pipe', label: '|', bytes: '|', aria: '竖线' },
  { id: 'tilde', label: '~', bytes: '~', aria: '波浪号' },
]

/** 长按重复：先等一段时间再连续发送，避免误触连发。 */
export const KEY_REPEAT_DELAY_MS = 450
export const KEY_REPEAT_INTERVAL_MS = 60

/**
 * 粘贴正文上限。与 orca 移动端一致取 256 KiB；超过就明确报错，不静默截断，
 * 因为截断后的半截命令一旦被用户回车提交，后果和粘错一样。
 */
export const PASTE_TEXT_LIMIT_BYTES = 256 * 1024

export function pasteTextTooLarge(text: string) {
  return new TextEncoder().encode(text).byteLength > PASTE_TEXT_LIMIT_BYTES
}

/**
 * 正文里的 ESC 一律替换成可见的 `␛`：herdr 的 bracketed-paste 分帧由服务端按目标
 * pane 当前状态决定，客户端不知道是否会包 `\x1b[200~…\x1b[201~`，只要正文里带着
 * 真的 ESC，就可能提前结束粘贴框架，让后面的字节被当成命令执行。
 */
export function sanitizePasteText(text: string) {
  return text.replaceAll('\x1b', '␛')
}
