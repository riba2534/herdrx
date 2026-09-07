import { useEffect, useRef, useState } from 'react'
import { Check, Copy } from 'lucide-react'
import { Button } from './ui'

export function Command({ title, value }: { title: string; value: string }) {
  const [copied, setCopied] = useState(false)
  const [failed, setFailed] = useState(false)
  const text = useRef<HTMLPreElement>(null)
  useEffect(() => {
    if (!copied) return
    const timer = setTimeout(() => setCopied(false), 2000)
    return () => clearTimeout(timer)
  }, [copied])
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true); setFailed(false)
    } catch {
      // LAN deployments may use HTTP, where the clipboard API is unavailable.
      const selection = window.getSelection(), range = document.createRange()
      range.selectNodeContents(text.current!)
      selection?.removeAllRanges(); selection?.addRange(range)
      setFailed(true)
    }
  }
  return <div className="setup-command">
    <div className="setup-command-heading"><span>{title}</span><Button type="button" className="button-ghost" aria-label={`复制${title}`} onClick={() => void copy()}>{copied ? <Check size={14}/> : <Copy size={14}/>}<span>{copied ? '已复制' : '复制'}</span></Button></div>
    <pre ref={text} tabIndex={0} aria-label={title}><code>{value}</code></pre>
    {failed && <small role="status">命令已选中，请使用系统菜单或 Ctrl/Cmd+C 复制。</small>}
  </div>
}
