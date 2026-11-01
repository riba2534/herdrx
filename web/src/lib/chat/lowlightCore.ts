// lowlight 的入口同时导出「全部语法」与「常用语法」；直接动态 import 它会把全部 190 多种
// 语法打进同一个 chunk。这里静态只取 createLowlight，让打包器摇掉其余部分，再由
// highlight.ts 动态加载本文件，各语言的语法另行按需加载。
import { createLowlight } from 'lowlight'

export const lowlight = createLowlight()
