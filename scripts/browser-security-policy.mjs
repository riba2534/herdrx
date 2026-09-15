import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { mkdtemp, rm } from 'node:fs/promises'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

// Mock browser fixtures must enforce the actual built website policy, not a
// permissive copy that silently stops matching production.
export async function websiteSecurityPolicy(binary) {
  const data = await mkdtemp(join(tmpdir(), 'herdrx-policy-'))
  let child
  let exited
  try {
    const probe = createServer()
    probe.listen(0, '127.0.0.1')
    await once(probe, 'listening')
    const port = probe.address().port
    await new Promise((done) => probe.close(done))
    const base = `http://127.0.0.1:${port}`
    // No production credentials, voice gateway or host configuration inherited.
    child = spawn(resolve(binary), [], {
      env: {
        PATH: process.env.PATH, HOME: data, TMPDIR: data,
        HERDRX_DATA_DIR: data, HERDRX_ADDR: `127.0.0.1:${port}`,
        HERDRX_PUBLIC_URL: base, HERDRX_COOKIE_SECURE: 'false',
      },
      stdio: 'ignore',
    })
    let failure
    child.on('error', (error) => { failure = error })
    exited = new Promise((done) => { child.once('exit', done); child.once('error', done) })
    for (let i = 0; i < 100; i++) {
      if (failure) throw failure
      if (child.exitCode !== null) throw new Error('Isolated policy website exited')
      const response = await fetch(base + '/', { signal: AbortSignal.timeout(1000) }).catch(() => null)
      if (response?.ok) {
        const csp = response.headers.get('content-security-policy')
        const permissions = response.headers.get('permissions-policy')
        await response.arrayBuffer()
        assert.ok(csp?.includes("script-src 'self'"), 'website must emit its production CSP')
        assert.ok(permissions, 'website must emit its permissions policy')
        return { csp, permissions }
      }
      await new Promise((done) => setTimeout(done, 100))
    }
    throw new Error('Isolated policy website did not become ready')
  } finally {
    if (child?.pid && child.exitCode === null) {
      child.kill('SIGTERM')
      const timer = setTimeout(() => child.kill('SIGKILL'), 3000)
      await exited
      clearTimeout(timer)
    }
    await rm(data, { recursive: true, force: true })
  }
}
