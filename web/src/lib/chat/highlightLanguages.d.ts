// highlight.js 的单语言入口没有自带类型声明；它们都默认导出一个语法定义函数。
declare module 'highlight.js/lib/languages/*' {
  import type { LanguageFn } from 'highlight.js'
  const language: LanguageFn
  export default language
}
