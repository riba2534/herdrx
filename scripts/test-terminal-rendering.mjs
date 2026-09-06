#!/usr/bin/env node
// Replay a synthetic fixed-column ANSI panel through the actual xterm DOM
// renderer and app CSS. jsdom cannot detect browser font-layout regressions.
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import { chromium } from '../web/node_modules/@playwright/test/index.mjs'

const root = new URL('../', import.meta.url)
const path = (relative) => fileURLToPath(new URL(relative, root))
const browser = await chromium.launch({
  headless: true,
  ...(process.env.HERDRX_TEST_CHROMIUM ? { executablePath: process.env.HERDRX_TEST_CHROMIUM } : {}),
})

try {
  for (const deviceScaleFactor of [1, 2]) {
    const page = await browser.newPage({ viewport: { width: 2600, height: 700 }, deviceScaleFactor })
    await page.setContent('<div class="workbench" style="display:block"><div class="terminal-host" id="terminal"></div></div>')
    await page.addStyleTag({ path: path('web/node_modules/@xterm/xterm/css/xterm.css') })
    await page.addStyleTag({ path: path('web/src/styles.css') })
    await page.addStyleTag({ path: path('web/src/ui-theme.css') })
    await page.addScriptTag({ path: path('web/node_modules/@xterm/xterm/lib/xterm.js') })
    await page.addScriptTag({ path: path('web/node_modules/@xterm/addon-unicode11/lib/addon-unicode11.js') })

    for (const fontSize of [14, 13.37, 10.2]) {
      const result = await page.evaluate(async (fontSize) => {
        window.testTerminal?.dispose()
        const terminal = new window.Terminal({
          allowProposedApi: true, cols: 295, rows: 12, fontSize, lineHeight: 1,
          fontFamily: '"JetBrains Mono", "SFMono-Regular", Consolas, monospace',
          scrollback: 0, convertEol: false,
          theme: { background: '#193448', foreground: '#eeeeee' },
        })
        terminal.loadAddon(new window.Unicode11Addon.Unicode11Addon())
        terminal.unicode.activeVersion = '11'
        terminal.open(document.getElementById('terminal'))
        window.testTerminal = terminal

        // Fullwidth punctuation must occupy the same columns whether isolated,
        // adjacent, bold or mixed with ASCII. A panel starts at column 206.
        const lines = [
          '',
          'Plain ASCII terminal output',
          '中文（English），状态（ready），请继续。',
          '（），。'.repeat(16),
          '\x1b[1m当前状态：正常（ready），连接已建立。\x1b[22m',
          '（中文）'.repeat(15),
          '\x1b[3m混排（italic），文字。\x1b[23m',
          '┌────────┬────────┐',
          '│中文，值│（测试）│',
          '└────────┴────────┘',
        ]
        const ansi = lines.map((text, row) =>
          `\x1b[${row + 1};1H\x1b[0m${text}\x1b[${row + 1};206H\x1b[48;2;38;38;38m${' '.repeat(90)}`,
        ).join('') + '\x1b[0m\x1b[?25l'
        await new Promise((done) => terminal.write(ansi, done))
        await document.fonts.ready
        await new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done)))

        const positions = lines.map((_, row) => {
          const line = terminal.buffer.active.getLine(row)
          let column = -1
          for (let x = 0; x < terminal.cols; x++) {
            const cell = line.getCell(x)
            if (cell.isBgRGB() && cell.getBgColor() === 0x262626) { column = x; break }
          }
          const element = document.querySelectorAll('.xterm-rows > div')[row]
          const panel = [...element.children].find((span) => span.style.backgroundColor === 'rgb(38, 38, 38)')
          return { column, left: panel ? panel.getBoundingClientRect().left - element.getBoundingClientRect().left : null }
        })
        return { positions }
      }, fontSize)

      assert.ok(result.positions.every(({ column, left }) => column === 205 && left !== null), 'fixed panel column was lost')
      const lefts = result.positions.map(({ left }) => left)
      const drift = Math.max(...lefts) - Math.min(...lefts)
      // The pinned DOM renderer measures repeated glyphs with integer
      // offsetWidth, leaving small subpixel accumulation across 205 columns.
      // Punctuation trimming regresses this fixture by tens/hundreds of pixels.
      assert.ok(drift < 4, `CJK punctuation shifted the panel by ${drift}px (font ${fontSize}, DPR ${deviceScaleFactor})`)
    }
    await page.close()
  }
  console.log('Terminal rendering: fixed-column panels align with CJK punctuation, bold/italic, fractional fonts and DPR 1/2.')
} finally {
  await browser.close()
}
