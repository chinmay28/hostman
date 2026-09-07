import { useState, type MouseEvent } from 'react'
import { createPortal } from 'react-dom'
import { launchable, openApp } from '../lib/launch'
import type { Installation } from '../types'
import { Sheet } from './ui'

/**
 * The button that opens a deployed app in the phone's browser. HostMan knows
 * an app is healthy and which port it answers on; this is the one tap that
 * turns knowing into using it.
 *
 * It shows nothing at all when the app has not said enough for HostMan to name
 * an address — a dead "Open" would be worse than none. Where the same app is
 * deployed to several hosts there is no right one to guess at, so it asks.
 */
export function LaunchButton({
  installations,
  className = 'open-chip',
  label = 'Open',
}: {
  /** The installations this button can open: one card's, or all of an app's. */
  installations: Installation[]
  className?: string
  label?: string
}) {
  const [picking, setPicking] = useState(false)
  const targets = launchable(installations)
  if (targets.length === 0) return null

  const press = (e: MouseEvent) => {
    // The button usually sits on a card that is itself a link to the app, and
    // opening the app is not navigating to its page.
    e.preventDefault()
    e.stopPropagation()
    if (targets.length === 1) {
      openApp(targets[0].url)
      return
    }
    setPicking(true)
  }

  const close = () => setPicking(false)

  return (
    <>
      <button
        className={className}
        onClick={press}
        title={targets.length === 1 ? targets[0].url : 'Open on…'}
      >
        <LaunchIcon />
        {label}
      </button>

      {/* Through a portal: the sheet is a full-screen overlay, and the card it
          was tapped on is usually a link that would swallow its clicks. */}
      {picking &&
        createPortal(
          <Sheet
            title={`Open ${targets[0].installation.appName || 'app'}`}
            subtitle="It is deployed to more than one host."
            onClose={close}
          >
            <div className="pick-list">
              {targets.map(({ installation, url }) => (
                <button
                  key={installation.id}
                  className="secondary"
                  onClick={() => {
                    openApp(url)
                    close()
                  }}
                >
                  <span className="grow">
                    <span className="title">{installation.hostName}</span>
                    <span className="sub">{url}</span>
                  </span>
                  <LaunchIcon />
                </button>
              ))}
            </div>
          </Sheet>,
          document.body,
        )}
    </>
  )
}

/** An arrow leaving its frame: the app opens away from HostMan, in the
 *  browser, rather than in the screen the button is on. */
export function LaunchIcon() {
  return (
    <svg
      className="launch-icon"
      viewBox="0 0 24 24"
      aria-hidden="true"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d="M13.5 4.5H19.5V10.5" />
      <path d="M19.5 4.5 11 13" />
      <path d="M18 14.5v4a1.5 1.5 0 0 1-1.5 1.5h-11A1.5 1.5 0 0 1 4 18.5v-11A1.5 1.5 0 0 1 5.5 6h4" />
    </svg>
  )
}
