// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) packages/maker-shared/src/interaction.ts@19dad9c225
//
// 高风险 Shell 命令的判定（sudo、递归删除、git push、curl | sh …）。免授权模式下命令已经
// 自动执行，这里只用来在工作记录里事后标红提示，不拦截、不改变任何执行。

// 与上游相比修正了递归删除：上游的 `rm\s+-[^\n;|&]*r\b` 认不出最常见的 `rm -rf x`（r 后面紧跟 f）。
const HIGH_RISK = /(^|[;&|]\s*)(sudo|rm\s+(?:-\w*r\w*|--recursive)|git\s+reset\s+--hard|git\s+push|curl\b[^\n|]*\|\s*(sh|bash)|wget\b[^\n|]*\|\s*(sh|bash)|chmod\s+-R|chown\s+-R|pkill|killall)\b/i

export function isHighRiskShellCommand(command: string) {
  return HIGH_RISK.test(command.trim())
}

export const HIGH_RISK_HINT = '这条命令可能修改系统、仓库或外部服务状态；免授权模式下它已自动执行，请确认结果符合预期。'
