import { Textarea, Input } from './Form'
import { useRef, useState } from 'react'
import { Upload } from 'lucide-react'
import { Button } from './ui'

export function PrivateKeyInput({ value, onChange, required = false, disabled = false }: { value: string; onChange: (text: string) => void; required?: boolean; disabled?: boolean }) {
  const fileInput = useRef<HTMLInputElement>(null)
  const [error, setError] = useState('')
  const [reading, setReading] = useState(false)
  const read = async (file?: File) => {
    if (!file || disabled) return
    setError('')
    if (file.size > 65536) { setError('私钥文件不能超过 64 KB'); return }
    setReading(true)
    try { const text = await file.text(); if (!text.includes('PRIVATE KEY-----')) throw new Error('请选择 OpenSSH 或 PEM 私钥文件，公钥文件不能用于登录'); onChange(text) }
    catch (reason) { setError(reason instanceof Error ? reason.message : '无法读取文件，请重试') }
    finally { setReading(false); if (fileInput.current) fileInput.current.value = '' }
  }
  return <div className="private-key-import" onDragOver={(event) => event.preventDefault()} onDrop={(event) => { event.preventDefault(); void read(event.dataTransfer.files[0]) }}>
    <label className="field"><span className="field-label">OpenSSH/PEM 私钥</span><Textarea aria-label="OpenSSH/PEM 私钥" className="input textarea key-input" rows={6} value={value} onChange={(event) => onChange(event.target.value)} required={required} disabled={disabled || reading} autoComplete="off" spellCheck={false} placeholder="粘贴私钥，或拖入私钥文件"/></label>
    <Input ref={fileInput} type="file" hidden aria-label="导入私钥文件" onChange={(event) => void read(event.target.files?.[0])}/>
    <div className="key-import-actions"><Button type="button" className="button-secondary" pending={reading} disabled={disabled} onClick={() => fileInput.current?.click()}><Upload size={16}/>导入私钥文件</Button><small className="field-hint">支持 OpenSSH / PEM，最大 64 KB</small></div>
    {error && <p className="field-error" role="alert">{error}</p>}
  </div>
}
