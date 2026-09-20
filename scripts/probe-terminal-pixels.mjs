#!/usr/bin/env node
// C2 measurement evidence from the pinned xterm DOM renderer. This deliberately
// does not add pixel fields to the production terminal protocol.
import assert from 'node:assert/strict'
import { mkdir, writeFile } from 'node:fs/promises'
import { dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium, firefox, webkit } from '../web/node_modules/@playwright/test/index.mjs'

const output = process.argv[2]
if (!output) throw new Error('usage: node scripts/probe-terminal-pixels.mjs OUTPUT.json')
const root = new URL('../', import.meta.url)
const path = relative => fileURLToPath(new URL(relative, root))
const measurements = []
for (const name of (process.env.HERDRX_TEST_ENGINES || 'chromium,firefox,webkit').split(',')) {
  const browser = await ({ chromium, firefox, webkit })[name].launch({ headless: true })
  try {
    for (const deviceScaleFactor of [1, 1.25, 2, 3]) {
      const context = await browser.newContext({ viewport: { width: 1200, height: 800 }, deviceScaleFactor })
      const page = await context.newPage()
      await page.setContent('<div id="terminal"></div>')
      await page.addStyleTag({ path: path('web/node_modules/@xterm/xterm/css/xterm.css') })
      await page.addScriptTag({ path: path('web/node_modules/@xterm/xterm/lib/xterm.js') })
      for (const fontSize of [10, 13.37, 14, 18]) {
        const measurement = await page.evaluate(async fontSize => {
          window.probeTerminal?.dispose()
          const terminal = new window.Terminal({ cols: 80, rows: 24, fontSize, fontFamily: 'monospace', lineHeight: 1, scrollback: 0 })
          terminal.open(document.getElementById('terminal'))
          window.probeTerminal = terminal
          await document.fonts.ready
          await new Promise(done => terminal.write('ASCII 中文', done))
          await new Promise(done => requestAnimationFrame(() => requestAnimationFrame(done)))
          const screen = document.querySelector('.xterm-screen').getBoundingClientRect()
          const row = document.querySelector('.xterm-rows > div').getBoundingClientRect()
          const dpr = window.devicePixelRatio
          return { fontSize, dpr, cols: terminal.cols, rows: terminal.rows,
            cssCellWidth: screen.width / terminal.cols, cssCellHeight: row.height,
            deviceCellWidth: screen.width / terminal.cols * dpr, deviceCellHeight: row.height * dpr,
            screenCSS: { width: screen.width, height: screen.height } }
        }, fontSize)
        assert.equal(measurement.cols, 80)
        assert.equal(measurement.rows, 24)
        assert.ok(measurement.cssCellWidth > 0 && measurement.cssCellHeight > 0)
        measurements.push({ browser: name, requestedDPR: deviceScaleFactor, ...measurement })
      }
      await context.close()
    }
  } finally { await browser.close() }
}
const results = {
  experiment: 'xterm-5.5 DOM renderer; synthetic browser DPR, not physical-device certification',
  fractionalDeviceWidths: measurements.filter(m => Math.abs(m.deviceCellWidth - Math.round(m.deviceCellWidth)) > 0.01).length,
  measurements,
  decision: 'disabled pending an explicit integer-cell rounding/unit contract and physical-device/cross-transport validation',
}
await mkdir(dirname(output), { recursive: true })
await writeFile(output, JSON.stringify(results, null, 2) + '\n')
console.log(JSON.stringify({ samples: measurements.length, fractionalDeviceWidths: results.fractionalDeviceWidths, decision: results.decision }))
