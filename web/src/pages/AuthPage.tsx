import { Form } from '../components/Form'
import { BrandLogo } from '../components/Brand'
import { InstallAppButton } from '../components/PWA'
import { useState, type FormEvent } from 'react'
import { KeyRound, ShieldCheck } from 'lucide-react'
import { useAuth } from '../auth'
import { APIError, api } from '../lib/api'
import { Button, Field } from '../components/ui'
import { AppearanceToggle } from '../components/AppearanceToggle'

export function AuthPage({ bootstrap }: { bootstrap: boolean }) {
  const auth = useAuth()
  const [email, setEmail] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [password, setPassword] = useState('')
  const [token, setToken] = useState('')
  const [registrationRequested, setRegistering] = useState(false)
  const registering = !bootstrap && auth.registration === 'invite' && registrationRequested
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (pending) return
    setPending(true)
    setError('')
    try {
      const result = bootstrap
        ? await api.bootstrap({ email, display_name: displayName, password, token })
        : registering
          ? await api.register({ email, display_name: displayName, password, invite_code: token })
          : await api.login({ email, password })
      auth.setAuthenticated(result.user)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '请求失败')
      if (reason instanceof APIError && ['registration_closed', 'registration_changed', 'already_bootstrapped'].includes(reason.code)) await auth.refresh()
    } finally { setPending(false) }
  }

  return <main className="auth-shell auth-entry">
    <header className="auth-topbar"><a className="brand" href="/"><BrandLogo/></a><AppearanceToggle/></header>
    <section className="auth-card" aria-labelledby="auth-title">
      <div className="auth-caption">你的终端，随处继续</div>
      <h1 id="auth-title">{bootstrap ? '初始化 herdrx' : registering ? '接受邀请' : '欢迎回来'}</h1>
      <p className="auth-intro">{bootstrap ? '创建可信实例的首位管理员。初始化令牌位于工作台主机的数据目录 bootstrap-token 文件。' : registering ? '使用管理员发给你的一次性邀请码创建账号。' : '连接你的 Herdr 主机，继续正在进行的工作。'}</p>
      {auth.notice && <p className="notice" role="status">{auth.notice}</p>}
      <Form onSubmit={submit} className="form-stack">
        {(bootstrap || registering) && <Field name="display_name" label="显示名称" autoComplete="name" value={displayName} onChange={(event) => setDisplayName(event.target.value)} required />}
        <Field name="email" label="邮箱" type="email" autoComplete="email" value={email} onChange={(event) => setEmail(event.target.value)} required />
        <Field name="password" label="密码" type="password" autoComplete={bootstrap || registering ? 'new-password' : 'current-password'} minLength={bootstrap || registering ? 12 : undefined} value={password} onChange={(event) => setPassword(event.target.value)} hint={bootstrap || registering ? '至少 12 个字符' : undefined} required />
        {(bootstrap || registering) && <Field name="token" label={bootstrap ? '初始化令牌' : '邀请码'} autoComplete="off" value={token} onChange={(event) => setToken(event.target.value)} required />}
        {error && <div className="notice notice-error" role="alert">{error}</div>}
        <Button type="submit" className="button-primary button-wide" pending={pending}>
          {bootstrap ? <ShieldCheck size={17} aria-hidden="true" /> : <KeyRound size={17} aria-hidden="true" />}
          {bootstrap ? '创建管理员' : registering ? '创建账号' : '登录'}
        </Button>
      </Form>
      {!bootstrap && auth.registration !== 'closed' && <button className="auth-switch" disabled={pending} onClick={() => { setRegistering((value) => !value); setError('') }}>{registering ? '已经有账号？返回登录' : '有邀请码？创建账号'}</button>}
      {!bootstrap && auth.registration === 'closed' && <p className="trust-note">当前注册已关闭，如需账号请联系管理员。</p>}
      <p className="trust-note">终端和 SSH 凭据由你的 herdrx 实例处理。请只使用你信任的部署。</p>
      <div className="pwa-entry"><InstallAppButton/></div>
    </section>
  </main>
}
