#!/usr/bin/env node
// Compare real xterm DOM output with and without the application stylesheet.
// xterm 5.5 targets half the configured contrast for dim cells (2.25 at 4.5).
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import { chromium, firefox, webkit } from '../web/node_modules/@playwright/test/index.mjs'
import { terminalThemes } from '../web/src/lib/themes.ts'

const path = (relative) => fileURLToPath(new URL(relative, new URL('../', import.meta.url)))
const luminance = (rgb) => rgb.map((channel) => channel / 255)
  .map((channel) => channel <= .04045 ? channel / 12.92 : ((channel + .055) / 1.055) ** 2.4)
  .reduce((total, channel, index) => total + channel * [.2126, .7152, .0722][index], 0)
const contrast = (foreground, background) => {
  const values = [luminance(foreground), luminance(background)].sort((a, b) => b - a)
  return (values[0] + .05) / (values[1] + .05)
}
const rgb = (hex) => [1, 3, 5].map((index) => parseInt(hex.slice(index, index + 2), 16))

for (const engine of process.env.HERDRX_TEST_ENGINES?.split(',') || ['chromium']) {
  const browser = await ({ chromium, firefox, webkit })[engine].launch()
  try {
    const page = await browser.newPage()
    await page.setContent('<div class="workbench" style="display:block"><div class="terminal-host" id="terminal"></div></div>')
    await page.addStyleTag({ path: path('web/node_modules/@xterm/xterm/css/xterm.css') })
    const styles = await page.addStyleTag({ path: path('web/src/styles.css') })
    const themeStyles = await page.addStyleTag({ path: path('web/src/ui-theme.css') })
    await page.addScriptTag({ path: path('web/node_modules/@xterm/xterm/lib/xterm.js') })

    for (const name of ['Cobalt2', 'Solarized Light', 'Catppuccin Latte']) {
      const theme = terminalThemes[name]
      for (const minimumContrastRatio of [1, 4.5]) {
        await page.evaluate(async ({ theme, minimumContrastRatio }) => {
          window.testTerminal?.dispose()
          const workbench = document.querySelector('.workbench')
          workbench.style.setProperty('--terminal', theme.background)
          workbench.style.setProperty('--terminal-ink', theme.foreground)
          const terminal = new window.Terminal({ cols: 60, rows: 8, theme, minimumContrastRatio })
          terminal.open(document.getElementById('terminal'))
          window.testTerminal = terminal
          await new Promise((done) => terminal.write('\x1b[?25lNORMAL\r\n\x1b[2mDIM\r\n\x1b[22;31mRED\r\n\x1b[2mDIMRED\r\n\x1b[22;37mWHITE\r\n\x1b[2mDIMWHITE\x1b[0m', done))
          await new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done)))
        }, { theme, minimumContrastRatio })
        const sample = () => page.evaluate((background) => {
          const canvas = document.createElement('canvas')
          canvas.width = canvas.height = 1
          const context = canvas.getContext('2d')
          return Object.fromEntries(['NORMAL', 'DIM', 'RED', 'DIMRED', 'WHITE', 'DIMWHITE'].map((label) => {
            const span = [...document.querySelectorAll('.xterm-rows span')].find((element) => element.textContent === label)
            if (!span) throw new Error(`missing rendered ANSI fixture ${label}`)
            const style = getComputedStyle(span)
            context.clearRect(0, 0, 1, 1)
            context.fillStyle = style.color
            context.fillRect(0, 0, 1, 1)
            const rgba = [...context.getImageData(0, 0, 1, 1).data]
            context.fillStyle = background
            context.fillRect(0, 0, 1, 1)
            context.fillStyle = style.color
            context.fillRect(0, 0, 1, 1)
            return [label, { color: style.color, opacity: style.opacity, rgba, effective: [...context.getImageData(0, 0, 1, 1).data].slice(0, 3) }]
          }))
        }, theme.background)
        for (const style of [styles, themeStyles]) await style.evaluate((element) => { element.sheet.disabled = true })
        const native = await sample()
        for (const style of [styles, themeStyles]) await style.evaluate((element) => { element.sheet.disabled = false })
        const actual = await sample()
        assert.deepEqual(actual, native, `${engine} ${name}: app CSS overrides xterm ANSI colors or minimum contrast (${minimumContrastRatio})`)
        for (const label of ['DIM', 'DIMRED', 'DIMWHITE']) assert.equal(actual[label].opacity, '1', 'dim must not also fade the cell background')
        if (minimumContrastRatio === 1) {
          for (const [label, color] of [['DIM', theme.foreground], ['DIMRED', theme.red], ['DIMWHITE', theme.white]]) {
            // Canvas round trips can round a translucent RGB channel by one.
            const expected = [...rgb(color), 128]
            assert.ok(actual[label].rgba.every((channel, index) => Math.abs(channel - expected[index]) <= 1), `${engine} ${name} ${label}: theme ANSI hue or native dim opacity was lost`)
          }
        } else {
          for (const label of ['NORMAL', 'RED', 'WHITE']) assert.ok(contrast(actual[label].effective, rgb(theme.background)) >= 4.5, `${engine} ${name} ${label}: enhanced normal text contrast is below 4.5`)
          // White is deliberately low-contrast in the light themes. Its dim
          // correction must survive CSS; xterm's dim target is 4.5 / 2.
          // Native xterm can leave other dim colors below that after alpha
          // blending, so compare all samples to native instead of promising
          // a contrast floor that the renderer itself does not guarantee.
          assert.ok(contrast(actual.DIMWHITE.effective, rgb(theme.background)) >= 2.25, `${engine} ${name}: enhanced dim white contrast is below 2.25`)
        }
        console.log(`${engine}: ${name}, contrast ${minimumContrastRatio}, dim default/red/white = ${['DIM', 'DIMRED', 'DIMWHITE'].map((label) => contrast(actual[label].effective, rgb(theme.background)).toFixed(2)).join('/')}; native ANSI colors preserved`)
      }
    }
  } finally {
    await browser.close()
  }
}
