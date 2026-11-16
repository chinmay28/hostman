// The Android app's page bridge (apps/android/app/src/main/assets/
// hostman-bridge.js) run against this client in jsdom, with the native side
// played here: what the page calls, the app must receive in the shape
// core/Bridge.kt parses, and the app's answers must come back to the page.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import { nativeApp } from './nativeApp'

// Tests run in apps/web.
const script = readFileSync(resolve('../android/app/src/main/assets/hostman-bridge.js'), 'utf8')

type Listener = (event: { data: string }) => void

/** Plays the app: records each message, and answers it as PageBridge does. */
function fakeApp(answer: (msg: Record<string, unknown>) => Record<string, unknown>) {
  const sent: Record<string, unknown>[] = []
  let listener: Listener | null = null
  const port = {
    addEventListener: (_: string, fn: Listener) => {
      listener = fn
    },
    postMessage: (text: string) => {
      const msg = JSON.parse(text) as Record<string, unknown>
      sent.push(msg)
      const reply = { re: msg.re, ok: true, ...answer(msg) }
      queueMicrotask(() => listener?.({ data: JSON.stringify(reply) }))
    },
  }
  ;(window as unknown as { HostManAndroid: unknown }).HostManAndroid = port
  // The app substitutes the mode before injecting the script.
  new Function(script.replace('"__HOSTMAN_MODE__"', '"on_phone"'))()
  return sent
}

describe('the page bridge', () => {
  afterEach(() => {
    delete (window as unknown as { HostManAndroid?: unknown }).HostManAndroid
    delete window.hostmanApp
  })

  it('does nothing without the app', () => {
    new Function(script)()
    expect(window.hostmanApp).toBeUndefined()
    expect(nativeApp()).toBeNull()
  })

  it('says the page is in the app, and in which mode', () => {
    fakeApp(() => ({}))
    expect(nativeApp()?.platform).toBe('android')
    expect(nativeApp()?.mode).toBe('on_phone')
  })

  it('hands a deployment to the app to follow', async () => {
    const sent = fakeApp((msg) => (msg.t === 'watch' ? { following: true } : {}))
    const result = await nativeApp()!.watchDeployment(41, 'CountRoster on nakedpi')
    expect(result).toEqual({ following: true })
    expect(sent).toEqual([{ t: 'watch', id: 41, title: 'CountRoster on nakedpi', re: 1 }])
  })

  it('opens the app settings', async () => {
    const sent = fakeApp(() => ({}))
    await nativeApp()!.openSettings()
    expect(sent[0]?.t).toBe('settings')
  })
})
