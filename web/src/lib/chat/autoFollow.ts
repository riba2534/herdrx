// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) apps/desktop/src/renderer/components/chat/autoFollowIntent.ts@19dad9c225
//
// 贴底跟随的「解除」看用户输入意图而不是距离：滚轮上滚、触摸下拉、PageUp 这些只有用户能产生，
// 滚一格立即停止跟随；内容在下方长高、布局收缩这些被动位移不算。

/** 已经贴死底部的距离：继续往下滚通常不再产生 scroll 事件，只能在 wheel 层恢复跟随。 */
const REPIN_AT_BOTTOM_PX = 8

/**
 * 垂直方向是主轴的上滚。触控板在横向可滚区域（代码块、diff）平移时常带微小的负 deltaY，
 * 不判主轴会被误当成上滚，随手一划就停掉跟随。
 */
export function isUpwardWheelIntent({ deltaX, deltaY }: { deltaX: number; deltaY: number }) {
  return deltaY < 0 && Math.abs(deltaY) >= Math.abs(deltaX)
}

/** 已贴底的向下滚轮：恢复跟随。 */
export function shouldRepinOnWheel({ deltaX, deltaY, distanceFromBottom }: { deltaX: number; deltaY: number; distanceFromBottom: number }) {
  return deltaY > 0 && Math.abs(deltaY) >= Math.abs(deltaX) && distanceFromBottom <= REPIN_AT_BOTTOM_PX
}

/**
 * 滚轮落在一个还能往上滚的内层滚动区（折叠的长输出、diff、代码块）里时，滚动的是那一块，
 * 不是对话记录本身，不解除跟随。
 */
export function insideScrollableAbove(target: EventTarget | null, root: Element) {
  for (let node = target instanceof Element ? target : null; node && node !== root; node = node.parentElement) {
    if (node.scrollTop > 0 && node.scrollHeight > node.clientHeight) {
      const overflow = getComputedStyle(node).overflowY
      if (overflow === 'auto' || overflow === 'scroll') return true
    }
  }
  return false
}
