import { render, screen } from '@testing-library/react'
import { expect, it } from 'vitest'
import { HostFolders, folderHostCount, hostInFolder, hostSearchText } from './HostFolders'
import type { Host, HostFolder } from '../types'

const folders: HostFolder[] = [
  { id: 'f-work', name: '公司', created_at: '', updated_at: '' },
  { id: 'f-work-sub', name: '测试环境', parent_id: 'f-work', created_at: '', updated_at: '' },
]
const hosts: Host[] = [
  { id: 'h1', name: '公司开发机', transport: 'ssh', hostname: 'dev.example.test', username: 'riba', folder_id: 'f-work', created_at: '', updated_at: '' },
  { id: 'h2', name: '节点', transport: 'ssh', hostname: '10.0.0.8', username: 'ubuntu', folder_id: 'f-work-sub', created_at: '', updated_at: '' },
]

it('counts hosts in a folder and its subfolders', () => {
  expect(folderHostCount('f-work', folders, hosts)).toBe(2)
  expect(folderHostCount('f-work-sub', folders, hosts)).toBe(1)
  expect(hostInFolder(hosts[1], 'f-work', folders)).toBe(true)
  expect(hostInFolder(hosts[0], 'f-work-sub', folders)).toBe(false)
})

it('includes username and folder path in search text', () => {
  expect(hostSearchText(hosts[1], '公司 / 测试环境').toLocaleLowerCase()).toContain('ubuntu')
  expect(hostSearchText(hosts[1], '公司 / 测试环境')).toContain('测试环境')
})

it('renders subtree counts on folder rows', () => {
  render(<HostFolders folders={folders} hosts={hosts} selected="*" onSelect={() => {}} onRefresh={async () => {}} />)
  expect(screen.getByRole('button', { name: /^公司/ })).toHaveTextContent('2')
  expect(screen.getByRole('button', { name: /^测试环境/ })).toHaveTextContent('1')
})
