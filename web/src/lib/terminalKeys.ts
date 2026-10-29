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

/**
 * 键栏上的粘滞修饰键：点一下只对下一个键生效（键栏按键或软键盘输入），生效后自动释放。
 * 手机软键盘没有 Ctrl / Alt，Shift 也不能和键栏上的方向键组合，所以由这里把修饰换算成
 * 终端认识的字节，规则与 xterm 默认（非 kitty 键盘协议）一致。
 */
export type StickyModifiers = { ctrl: boolean; alt: boolean; shift: boolean }
export const NO_MODIFIERS: StickyModifiers = { ctrl: false, alt: false, shift: false }

export const STICKY_MODIFIER_KEYS: { id: keyof StickyModifiers; label: string; aria: string }[] = [
  { id: 'ctrl', label: 'Ctrl', aria: 'Ctrl 修饰键，作用于下一个键' },
  { id: 'alt', label: 'Alt', aria: 'Alt 修饰键，作用于下一个键' },
  { id: 'shift', label: '⇧', aria: 'Shift 修饰键，作用于下一个键' },
]

export function hasModifiers(modifiers: StickyModifiers) {
  return modifiers.ctrl || modifiers.alt || modifiers.shift
}

/** xterm 的修饰参数：1 + Shift(1) + Alt(2) + Ctrl(4)。 */
function modifierParameter({ ctrl, alt, shift }: StickyModifiers) {
  return 1 + (shift ? 1 : 0) + (alt ? 2 : 0) + (ctrl ? 4 : 0)
}

/** 单个可打印字符加 Ctrl：字母、@ [ \ ] ^ _ 与空格映射到 C0 控制字符，? 映射到 DEL。 */
function controlCharacter(char: string) {
  if (char === ' ') return '\x00'
  if (char === '?') return '\x7f'
  const code = char.toUpperCase().charCodeAt(0)
  return code >= 0x40 && code <= 0x5f ? String.fromCharCode(code & 0x1f) : char
}

/** 方向键（普通或应用光标模式）的结尾字母 A–D；不是方向键时为空。 */
function arrowFinal(data: string) {
  return data.length === 3 && data[0] === '\x1b' && (data[1] === '[' || data[1] === 'O') && 'ABCD'.includes(data[2]) ? data[2] : ''
}

/**
 * 是否是“一个键”的字节。修饰键只被这样的输入消耗；xterm 自己回报的焦点、
 * 设备属性等序列和多字符输入不会把按下的 Ctrl 用掉。
 */
export function isModifiableKey(data: string) {
  return Boolean(arrowFinal(data)) || data === '\x1b[3~' || data === '\x1b[Z' || [...data].length === 1
}

/**
 * 需要交给 Herdr 按键接口而不是写字节的组合。硬件 Shift+Enter 在终端里走
 * `pane.send_keys(['shift+enter'])`，由 Herdr 按目标 pane 的键盘协议编码（多数 Agent
 * 把它当换行）；传统字节里没有它，粘滞 ⇧+Enter 必须走同一条路，否则会直接提交。
 */
export function herdrKeyFor(data: string, { ctrl, alt, shift }: StickyModifiers) {
  return data === '\r' && shift && !ctrl && !alt ? 'shift+enter' : ''
}

/**
 * 把一个按键的原始字节换成带修饰的字节；herdrKeyFor 命中的组合先由调用方处理。只处理“一个键”：方向键与 Del 走
 * `CSI 1;m X` / `CSI 3;m ~`；Tab、回车、退格、Esc 和单个字符按传统终端规则；
 * Alt 一律是前缀 ESC。多字符输入（输入法上屏、粘贴）不是一个键，原样返回。
 */
export function applyModifiers(data: string, modifiers: StickyModifiers) {
  if (!hasModifiers(modifiers) || !isModifiableKey(data)) return data
  const { ctrl, alt, shift } = modifiers
  const meta = alt ? '\x1b' : ''
  const arrow = arrowFinal(data)
  if (arrow) return `\x1b[1;${modifierParameter(modifiers)}${arrow}`
  if (data === '\x1b[3~') return `\x1b[3;${modifierParameter(modifiers)}~`
  if (data === '\t') return meta + (shift ? '\x1b[Z' : '\t')
  if (data === '\x7f') return meta + (ctrl ? '\x08' : '\x7f')
  let char = data
  if (char.length > 1 || char.charCodeAt(0) < 0x20) return meta + char
  if (shift) char = char.toUpperCase()
  if (ctrl) char = controlCharacter(char)
  return meta + char
}
