import { describe, expect, it } from 'vitest'
import { createClientID } from './workbench'

describe('createClientID', () => {
  it('returns a UUID-shaped identifier', () => {
    expect(createClientID()).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
  })

  it('does not reuse identifiers', () => {
    expect(createClientID()).not.toBe(createClientID())
  })
})
