import { describe, expect, it } from 'vitest'
import { forwardFields, forwardInput, forwardSummary, forwardURL } from './forward'
import type { Forward } from '../types'

describe('forwardInput', () => {
  it('sends zeros when forwarding is off, whatever is typed', () => {
    expect(forwardInput({ enabled: false, listen: '18080', target: '80' })).toEqual({
      forwardPort: 0,
      forwardTargetPort: 0,
    })
  })

  it('sends the ports as numbers', () => {
    expect(forwardInput({ enabled: true, listen: ' 18080 ', target: '80' })).toEqual({
      forwardPort: 18080,
      forwardTargetPort: 80,
    })
  })

  it('sends 0 for a field that is not a whole number, for the server to reject', () => {
    expect(forwardInput({ enabled: true, listen: '18080', target: 'eighty' })).toEqual({
      forwardPort: 18080,
      forwardTargetPort: 0,
    })
  })
})

describe('forwardFields', () => {
  it('round-trips through forwardInput', () => {
    const app = { forwardPort: 18080, forwardTargetPort: 80 }
    expect(forwardInput(forwardFields(app))).toEqual(app)
  })

  it('reads an unforwarded app as off with empty fields', () => {
    expect(forwardFields({ forwardPort: 0, forwardTargetPort: 0 })).toEqual({
      enabled: false,
      listen: '',
      target: '',
    })
  })
})

describe('forwardURL', () => {
  it('uses the host this page was loaded from', () => {
    expect(forwardURL(18080, 'nakedpi.local')).toBe('http://nakedpi.local:18080/')
    expect(forwardURL(18080, '100.101.102.103')).toBe('http://100.101.102.103:18080/')
  })

  it('leaves out port 80 and brackets IPv6', () => {
    expect(forwardURL(80, 'hostman.local')).toBe('http://hostman.local/')
    expect(forwardURL(18080, 'fd7a::1')).toBe('http://[fd7a::1]:18080/')
  })
})

describe('forwardSummary', () => {
  const base: Forward = { appId: 1, listenPort: 18080, targetPort: 80, hostName: 'photoframe', state: 'listening', active: 0 }

  it('says where a working forward relays to', () => {
    expect(forwardSummary(base)).toEqual({ tone: 'good', text: 'Relaying to photoframe:80' })
    expect(forwardSummary({ ...base, active: 2 }).text).toBe('Relaying to photoframe:80 · 2 open connections')
  })

  it('warns when the last relayed connection failed', () => {
    const s = forwardSummary({ ...base, detail: 'connect to photoframe: no route to host' })
    expect(s.tone).toBe('warn')
    expect(s.text).toContain('no route to host')
  })

  it('reports waiting and errors with the server’s reason', () => {
    expect(forwardSummary({ ...base, state: 'waiting', detail: 'not installed on any host yet' })).toEqual({
      tone: 'warn',
      text: 'Waiting: not installed on any host yet',
    })
    expect(forwardSummary({ ...base, state: 'error', detail: 'port 18080 is already in use' }).tone).toBe('bad')
  })
})
