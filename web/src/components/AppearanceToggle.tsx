import { Moon, Sun } from 'lucide-react'
import { useAppearance, type AppearanceScope } from '../lib/appearance'
import { Select, SelectOption } from './Select'

export function AppearanceToggle({ scope = 'site' }: { scope?: AppearanceScope }) {
  const [appearance, setAppearance] = useAppearance(scope)
  const light = appearance !== 'dark'
  const label = light ? '浅色' : '深色'
  const action = light ? '切换为深色' : '切换为浅色'
  return <div className="appearance-controls">{scope === 'workbench' && <Select aria-label="工作台配色" className="input" value={appearance} onChange={(event) => {
    const value = event.target.value
    if (value === 'light' || value === 'dark' || value === 'solarized-light') setAppearance(value)
  }}><SelectOption value="dark">经典深色</SelectOption><SelectOption value="light">经典浅色</SelectOption><SelectOption value="solarized-light">Solarized Light（暖纸色）</SelectOption></Select>}<button type="button" className="button button-ghost appearance-toggle" aria-label={action} data-tooltip={`当前为${label}，${action}`} onClick={() => setAppearance(light ? 'dark' : 'light')}>
    {light ? <Sun size={16} aria-hidden="true"/> : <Moon size={16} aria-hidden="true"/>}
    <span className="appearance-label">{label}</span>
  </button></div>
}
