// 代码块高亮：lowlight 核心与每种语言的语法都是独立的懒加载 chunk，首屏一个都不加载；
// 只在页面上真的出现某种语言的代码块时才取那一种。输出是 hast 树（不是 HTML 字符串），
// 由组件逐节点转成 React 元素，日志里的代码不会被当作 HTML 解析。

import type { LanguageFn } from 'highlight.js'
import type { lowlight as Core } from './lowlightCore'

type Lowlight = typeof Core
export type HighlightTree = ReturnType<Lowlight['highlight']>

const LANGUAGES: Record<string, () => Promise<{ default: LanguageFn }>> = {
  bash: () => import('highlight.js/lib/languages/bash'),
  c: () => import('highlight.js/lib/languages/c'),
  cpp: () => import('highlight.js/lib/languages/cpp'),
  csharp: () => import('highlight.js/lib/languages/csharp'),
  css: () => import('highlight.js/lib/languages/css'),
  diff: () => import('highlight.js/lib/languages/diff'),
  dockerfile: () => import('highlight.js/lib/languages/dockerfile'),
  go: () => import('highlight.js/lib/languages/go'),
  ini: () => import('highlight.js/lib/languages/ini'),
  java: () => import('highlight.js/lib/languages/java'),
  javascript: () => import('highlight.js/lib/languages/javascript'),
  json: () => import('highlight.js/lib/languages/json'),
  kotlin: () => import('highlight.js/lib/languages/kotlin'),
  lua: () => import('highlight.js/lib/languages/lua'),
  makefile: () => import('highlight.js/lib/languages/makefile'),
  markdown: () => import('highlight.js/lib/languages/markdown'),
  php: () => import('highlight.js/lib/languages/php'),
  python: () => import('highlight.js/lib/languages/python'),
  ruby: () => import('highlight.js/lib/languages/ruby'),
  rust: () => import('highlight.js/lib/languages/rust'),
  scss: () => import('highlight.js/lib/languages/scss'),
  sql: () => import('highlight.js/lib/languages/sql'),
  swift: () => import('highlight.js/lib/languages/swift'),
  typescript: () => import('highlight.js/lib/languages/typescript'),
  xml: () => import('highlight.js/lib/languages/xml'),
  yaml: () => import('highlight.js/lib/languages/yaml'),
}

const ALIASES: Record<string, string> = {
  sh: 'bash', shell: 'bash', zsh: 'bash', console: 'bash', shellsession: 'bash',
  h: 'c', 'c++': 'cpp', cc: 'cpp', cxx: 'cpp', hpp: 'cpp', cs: 'csharp',
  patch: 'diff', docker: 'dockerfile', golang: 'go', toml: 'ini', conf: 'ini',
  js: 'javascript', jsx: 'javascript', mjs: 'javascript', cjs: 'javascript',
  jsonc: 'json', json5: 'json', kt: 'kotlin', make: 'makefile', mk: 'makefile', md: 'markdown',
  py: 'python', python3: 'python', rb: 'ruby', rs: 'rust', ts: 'typescript', tsx: 'typescript',
  html: 'xml', htm: 'xml', svg: 'xml', yml: 'yaml',
}

/** 代码块语言 → 支持的语法名；不认识的语言按纯文本显示。 */
export function highlightLanguage(language: string) {
  const name = language.trim().toLowerCase()
  const resolved = ALIASES[name] ?? name
  return Object.hasOwn(LANGUAGES, resolved) ? resolved : null
}

let core: Promise<Lowlight> | null = null
const registered = new Map<string, Promise<void>>()

/** 高亮一段代码；语言不支持或加载失败时返回 null，调用方保持纯文本。 */
export async function highlightCode(code: string, language: string): Promise<HighlightTree | null> {
  const name = highlightLanguage(language)
  if (!name) return null
  try {
    core ??= import('./lowlightCore').then((module) => module.lowlight)
    const lowlight = await core
    let ready = registered.get(name)
    if (!ready) {
      ready = LANGUAGES[name]().then((module) => lowlight.register(name, module.default))
      registered.set(name, ready)
    }
    await ready
    return lowlight.highlight(name, code)
  } catch {
    registered.delete(name)
    return null
  }
}
