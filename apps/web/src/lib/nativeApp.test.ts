import { afterEach, describe, expect, it, vi } from 'vitest'
import { followInApp, nativeApp, resetFollowed, type HostmanApp } from './nativeApp'

function installApp(): HostmanApp & { watchDeployment: ReturnType<typeof vi.fn> } {
  const app = {
    platform: 'android' as const,
    mode: 'remote' as const,
    openSettings: vi.fn(() => Promise.resolve()),
    watchDeployment: vi.fn(() => Promise.resolve({ following: true })),
  }
  window.hostmanApp = app
  return app
}

describe('the Android app hook', () => {
  afterEach(() => {
    delete window.hostmanApp
    resetFollowed()
  })

  it('is absent in a browser, and following does nothing there', () => {
    expect(nativeApp()).toBeNull()
    expect(() => followInApp({ id: 4, status: 'running' })).not.toThrow()
  })

  it('ignores something that is not the app', () => {
    ;(window as unknown as { hostmanApp: unknown }).hostmanApp = { platform: 'android' }
    expect(nativeApp()).toBeNull()
  })

  it('hands a running deployment over once, with a title', () => {
    const app = installApp()
    const dep = { id: 41, status: 'running', appName: 'CountRoster', hostName: 'nakedpi' }
    followInApp(dep)
    followInApp(dep)
    expect(app.watchDeployment).toHaveBeenCalledTimes(1)
    expect(app.watchDeployment).toHaveBeenCalledWith(41, 'CountRoster on nakedpi')
  })

  it('leaves a finished deployment alone', () => {
    const app = installApp()
    followInApp({ id: 42, status: 'succeeded' })
    followInApp({ id: 43, status: 'failed' })
    expect(app.watchDeployment).not.toHaveBeenCalled()
  })

  it('asks again after the app refused', async () => {
    const app = installApp()
    app.watchDeployment.mockImplementationOnce(() => Promise.reject(new Error('no')))
    followInApp({ id: 44, status: 'running' })
    await Promise.resolve()
    await Promise.resolve()
    followInApp({ id: 44, status: 'running' })
    expect(app.watchDeployment).toHaveBeenCalledTimes(2)
    expect(app.watchDeployment).toHaveBeenLastCalledWith(44, '')
  })
})
