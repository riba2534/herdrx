import { Form } from '../components/Form'
import { BrandLogo } from '../components/Brand'
import { useState, type FormEvent } from 'react'
import { ArrowLeft, KeyRound, LogOut } from 'lucide-react'
import { useAuth } from '../auth'
import { api } from '../lib/api'
import { navigate } from '../lib/navigation'
import { MIN_PASSWORD_LENGTH, resubscribeThisDevice } from '../lib/password'
import { Button, Field } from '../components/ui'
import { AppearanceToggle } from '../components/AppearanceToggle'
import type { User } from '../types'

/** Form checks empty fields and length; these rules compare two fields. */
function passwordFormError(current: string, next: string, confirmation: string) {
  if (next === current) return '新密码不能与当前密码相同，请换一个新密码'
  if (next !== confirmation) return '两次输入的新密码不一致，请重新输入确认密码'
  return ''
}

/**
 * Self-service password change. With `forced` the administrator reset this
 * account: the workbench stays closed until the password is changed, and the
 * only other action is logging out.
 */
export function AccountPage({ forced = false }: { forced?: boolean }) {
  const auth = useAuth()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [pending, setPending] = useState(false)
  const [signingOut, setSigningOut] = useState(false)
  const [error, setError] = useState('')
  const [done, setDone] = useState<{ user: User; push: Awaited<ReturnType<typeof resubscribeThisDevice>> } | null>(null)

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (pending) return
    const invalid = passwordFormError(current, next, confirmation)
    setError(invalid)
    if (invalid) return
    setPending(true)
    try {
      const result = await api.changePassword({ current_password: current, new_password: next })
      setCurrent(''); setNext(''); setConfirmation('')
      // The change ended every push subscription of the account; keep this device's.
      setDone({ user: result.user, push: await resubscribeThisDevice() })
      // A forced change waits for the user to continue, so the result stays visible.
      if (!forced) auth.setAuthenticated(result.user)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '密码未修改，请重试')
    } finally { setPending(false) }
  }

  const signOut = async () => {
    setSigningOut(true); setError('')
    try { await auth.signOut() }
    catch (reason) { setError(reason instanceof Error ? reason.message : '退出未完成，请重试') }
    finally { setSigningOut(false) }
  }

  return <main className="auth-shell auth-entry">
    <header className="auth-topbar">
      <a className="brand" href="/" onClick={(event) => { event.preventDefault(); if (!forced) navigate('/') }}><BrandLogo/></a>
      <div className="auth-topbar-actions">
        <AppearanceToggle/>
        {forced
          ? <Button className="button-ghost" pending={signingOut} onClick={() => void signOut()}><LogOut size={16}/>退出登录</Button>
          : <Button className="button-ghost" onClick={() => navigate('/')}><ArrowLeft size={16}/>返回主机</Button>}
      </div>
    </header>
    <section className="auth-card" aria-labelledby="account-title">
      <div className="auth-caption">{auth.user?.display_name} · {auth.user?.email}</div>
      <h1 id="account-title">{forced ? '请先修改密码' : '修改密码'}</h1>
      <p className="auth-intro">{forced
        ? '管理员已为你重置密码。设置新密码后才能进入工作台，远程 Herdr 和任务不受影响。'
        : '修改后，其他设备上的 Web 登录会退出并停止通知，当前设备保持登录。远程 Herdr 和任务不受影响。'}</p>
      {done
        ? <>
          <p className="notice" role="status">密码已修改，其他设备已退出登录并停止通知。{done.push === 'failed' && '本设备的通知未能恢复，如需通知请在本设备重新开启。'}</p>
          {forced && <Button className="button-primary button-wide" onClick={() => { navigate('/'); auth.setAuthenticated(done.user) }}>进入工作台</Button>}
        </>
        : <Form onSubmit={submit} className="form-stack" aria-label="修改密码">
          <Field name="current_password" label={forced ? '临时密码' : '当前密码'} type="password" autoComplete="current-password" value={current} onChange={(event) => setCurrent(event.target.value)} required />
          <Field name="new_password" label="新密码" type="password" autoComplete="new-password" minLength={MIN_PASSWORD_LENGTH} value={next} onChange={(event) => setNext(event.target.value)} hint={`至少 ${MIN_PASSWORD_LENGTH} 个字符`} required />
          <Field name="confirm_password" label="确认新密码" type="password" autoComplete="new-password" minLength={MIN_PASSWORD_LENGTH} value={confirmation} onChange={(event) => setConfirmation(event.target.value)} required />
          <Button type="submit" className="button-primary button-wide" pending={pending}><KeyRound size={17} aria-hidden="true"/>修改密码</Button>
        </Form>}
      {error && <div className="notice notice-error" role="alert">{error}</div>}
    </section>
  </main>
}
