/**
 * The HostMan Android app, when the page is running inside it.
 *
 * The app shows this very page in a web view and adds `window.hostmanApp`
 * before the page's own scripts run (apps/android/app/src/main/assets/
 * hostman-bridge.js). In a browser it is absent, and everything here is a
 * no-op: nothing the page does changes outside the app.
 */
export interface HostmanApp {
  platform: 'android'
  /** "remote": HostMan is on another machine; "on_phone": it is the phone. */
  mode: 'remote' | 'on_phone'
  /** Opens the app's own settings screen. */
  openSettings(): Promise<unknown>
  /** Asks the app to follow a deployment in a notification until it ends. */
  watchDeployment(id: number, title: string): Promise<{ following: boolean }>
}

declare global {
  interface Window {
    hostmanApp?: HostmanApp
  }
}

/** The app around this page, or null in a browser. */
export function nativeApp(): HostmanApp | null {
  const app = typeof window === 'undefined' ? undefined : window.hostmanApp
  return app && typeof app.watchDeployment === 'function' ? app : null
}

/** Deployments this page already asked the app to follow. */
const asked = new Set<number>()

/**
 * Hands a running deployment to the app, once per deployment per page load,
 * so its progress and outcome keep showing in a notification after the
 * person leaves. Inside a browser, or for a deployment that has ended, it does
 * nothing. Never throws: following is a convenience, not the page's job.
 */
export function followInApp(deployment: {
  id: number
  status: string
  appName?: string
  hostName?: string
}): void {
  const app = nativeApp()
  if (!app || deployment.status !== 'running' || asked.has(deployment.id)) return
  asked.add(deployment.id)
  const title =
    deployment.appName && deployment.hostName ? `${deployment.appName} on ${deployment.hostName}` : ''
  app.watchDeployment(deployment.id, title).catch(() => asked.delete(deployment.id))
}

/** For tests: forget which deployments were handed over. */
export function resetFollowed(): void {
  asked.clear()
}
