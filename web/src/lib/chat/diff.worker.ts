// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) apps/desktop/src/renderer/lib/agent-actions/diff.worker.ts@19dad9c225
//
// 大段 Edit / MultiEdit 的逐行比较放在 Worker 里做；只依赖纯函数，没有任何页面权限。

import { computeDiffDetails, type DiffSegment } from './diffStats'

self.addEventListener('message', (event: MessageEvent<{ id: string; segments: DiffSegment[] }>) => {
  const { id, segments } = event.data ?? {}
  if (!id || !Array.isArray(segments)) return
  try {
    postMessage({ id, segments: segments.map((segment) => ({ key: segment.key, details: computeDiffDetails(segment.oldString, segment.newString) })) })
  } catch {
    postMessage({ id })
  }
})
