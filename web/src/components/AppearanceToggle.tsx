import { Moon, Sun } from 'lucide-react'
import { useAppearance, type AppearanceScope } from '../lib/appearance'

export function AppearanceToggle({ scope = 'site' }: { scope?: AppearanceScope }) {
  const [appearance, setAppearance] = useAppearance(scope)
  const label = appearance === 'light' ? '浅色' : '深色'
  const action = appearance === 'light' ? '切换为深色' : '切换为浅色'
  return <button type="button" className="button button-ghost appearance-toggle" aria-label={action} data-tooltip={`当前为${label}，${action}`} onClick={() => setAppearance(appearance === 'light' ? 'dark' : 'light')}>
    {appearance === 'light' ? <Sun size={16} aria-hidden="true"/> : <Moon size={16} aria-hidden="true"/>}
    <span className="appearance-label">{label}</span>
  </button>
}
