import { describe, expect, it } from 'vitest'
import {
  agentNotificationTitle,
  agentStatusLabel,
  connectionLabel,
  contextMenuLabel,
  hostConnectionText,
  paneDisplayName,
  tabCountLabel,
  terminalCountLabel,
  transportLabel,
  workspaceCountLabel,
} from './labels'

describe('workbench labels', () => {
  it('maps connection, transport and agent status to Chinese', () => {
    expect(connectionLabel('connecting')).toBe('连接中')
    expect(connectionLabel('ready')).toBe('已连接')
    expect(connectionLabel('degraded')).toBe('连接受限')
    expect(connectionLabel('offline')).toBe('已断开')
    expect(transportLabel('ssh')).toBe('SSH')
    expect(transportLabel('tailcat')).toBe('Tailcat')
    expect(transportLabel('local')).toBe('本机')
    expect(agentStatusLabel('idle')).toBe('空闲')
    expect(agentStatusLabel('working')).toBe('运行中')
    expect(agentStatusLabel('blocked')).toBe('等待确认')
    expect(agentStatusLabel('done')).toBe('已完成')
  })

  it('formats counts, notifications and host connection text', () => {
    expect(workspaceCountLabel(2, 2)).toBe('2 个终端 · 2 个标签页')
    expect(terminalCountLabel(3)).toBe('3 个终端')
    expect(tabCountLabel(1)).toBe('1 个标签页')
    expect(agentNotificationTitle('Claude', 'blocked')).toBe('Claude 等待你的确认')
    expect(agentNotificationTitle('Claude', 'done')).toBe('Claude 已完成')
    expect(hostConnectionText('ready', 'ssh')).toBe('SSH')
    expect(hostConnectionText('degraded')).toBe('连接受限')
    expect(hostConnectionText('ready', 'local', '无法读取主机列表，请重新连接')).toBe('主机列表加载失败')
    expect(contextMenuLabel('pane')).toBe('终端右键菜单')
    expect(paneDisplayName({ pane_id: 'p1', label: '前端' })).toBe('前端')
    expect(paneDisplayName({ pane_id: 'p1', agent: 'claude' })).toBe('claude')
  })
})
