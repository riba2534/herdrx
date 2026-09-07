import { useEffect, useState } from 'react'
import { api, type CLIRelease, type RelayOffer } from '../lib/api'
import { Command } from './SetupCommand'

export function supportsWorkbenchRelay(version?: string) {
  const match = /^v(\d+)\.(\d+)\.(\d+)(?:-rc\.(\d+))?$/.exec(version || '')
  if (!match) return false
  const [, major, minor, patch, rc] = match
  return Number(major) > 0 || Number(minor) > 1 || (Number(minor) === 1 && (Number(patch) > 0 || !rc || Number(rc) >= 2))
}

function quote(value: string) { return `'${value.replaceAll("'", "'\\''")}'` }

export function RelayConnectCommand({ release, refresh = false }: { release?: CLIRelease | null; refresh?: boolean }) {
  const [offer, setOffer] = useState<RelayOffer | null>(null)
  const [loading, setLoading] = useState(true)
  const [revision, setRevision] = useState(0)
  useEffect(() => {
    let active = true
    setLoading(true)
    void (release === undefined ? api.cliRelease() : Promise.resolve(release)).then(async (available) => {
      if (available?.status !== 'available' || !supportsWorkbenchRelay(available.version)) return null
      return await api.relayOffer()
    }).then((value) => { if (active) setOffer(value) }).catch(() => {
      if (active) setOffer(null)
    }).finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [release, revision])
  useEffect(() => {
    if (!offer?.expires_at) return
    const delay = Date.parse(offer.expires_at) - Date.now() - 30_000
    if (!Number.isFinite(delay) || delay <= 0) return
    const timer = setTimeout(() => { setOffer(null); setRevision((value) => value + 1) }, delay)
    return () => clearTimeout(timer)
  }, [offer])
  let command = `~/.local/bin/herdrx connect${refresh ? ' --refresh-endpoint' : ''} --plain`
  if (offer?.available && offer.workbench?.startsWith('https://') && /^[\w-]{32,128}$/.test(offer.token || '') && Date.parse(offer.expires_at || '') > Date.now()) {
    command += ` --workbench ${quote(offer.workbench)} --relay-token ${quote(offer.token!)}`
    if (offer.address && /^[\dA-Fa-f:.]+$/.test(offer.address)) command += ` --relay-address ${quote(offer.address)}`
  }
  return <>
    {loading ? <p className="field-hint" role="status">正在准备连接命令…</p> : <Command title={refresh ? '生成端点更新包' : '生成绑定凭据'} value={command}/>}
    {offer?.available && <p className="field-hint">优先使用工作台中继，连不通时自动回退公共中继。请使用刚安装的 CLI 执行；命令包含临时授权，请勿分享。</p>}
  </>
}
