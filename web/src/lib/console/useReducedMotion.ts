import { useEffect, useState } from 'react'

const QUERY = '(prefers-reduced-motion: reduce)'

function reduced() {
  try { return typeof window.matchMedia === 'function' && window.matchMedia(QUERY).matches } catch { return false }
}

/** 系统「减弱动态效果」偏好；变化时跟着更新。 */
export function useReducedMotion() {
  const [value, setValue] = useState(reduced)
  useEffect(() => {
    if (typeof window.matchMedia !== 'function') return
    const media = window.matchMedia(QUERY)
    const update = () => setValue(media.matches)
    media.addEventListener?.('change', update)
    return () => media.removeEventListener?.('change', update)
  }, [])
  return value
}
