import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import type { SelfInfo } from '../types'
import { SelfCard } from './Settings'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const base: SelfInfo = {
  version: 'v2026.11.80',
  machineId: '',
  host: null,
  app: null,
  ref: 'main',
  runningDeploymentId: null,
  ready: false,
}

describe('the "This machine" card', () => {
  let host: HTMLDivElement
  let root: Root

  beforeEach(() => {
    host = document.createElement('div')
    document.body.appendChild(host)
    root = createRoot(host)
  })

  afterEach(() => {
    act(() => root.unmount())
    host.remove()
  })

  const render = (info: SelfInfo) =>
    act(() => {
      root.render(
        <MemoryRouter>
          <SelfCard info={info} onStarted={() => {}} />
        </MemoryRouter>,
      )
    })

  it('offers to add the machine when it is not registered', () => {
    render({ ...base, blocked: "This machine isn't registered as a host yet." })
    expect(host.textContent).toContain('Add this machine')
  })

  // A HostMan inside the Android app has no machine of its own to add: saying
  // "add 127.0.0.1" there would send the person after an sshd the phone lacks.
  it('says what updates HostMan when something else manages it', () => {
    render({
      ...base,
      managedBy: 'the HostMan Android app',
      blocked: 'HostMan here is installed and updated by the HostMan Android app, not by itself: update that instead.',
    })
    expect(host.textContent).toContain('the HostMan Android app')
    expect(host.textContent).not.toContain('Add this machine')
    expect(host.querySelector('button')).toBeNull()
  })
})
