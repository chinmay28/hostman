import { useEffect, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { Link, NavLink, useNavigate } from 'react-router-dom'
import { APP_VERSION } from '../version'

/** How long the developer badge stays on screen when the header mark is
 * tapped. Kept in sync with the `dev-flash*` animations in styles.css — the CSS
 * fades out on its own clock, this unmounts it afterwards. */
const DEV_FLASH_MS = 3000

/** The header the four tabs share. App renders it once, outside the routes, so
 * moving between tabs leaves the brand lockup and the developer mark exactly
 * where they were instead of rebuilding an identical header. Which tab you are
 * on is the tab bar's job to say; the header's job is to stay put. Adding
 * belongs to the Fab, in thumb reach at the bottom — the header carries
 * identity only. */
export function AppHeader() {
  return (
    <header className="header">
      <img className="brand-logo" src="/icon.svg" alt="" aria-hidden="true" />
      {/* Name over version, as a lockup — the version reads as part of the
          name rather than as another thing on the screen. */}
      <div className="brand">
        <h1>HostMan</h1>
        <span className="brand-version">{APP_VERSION}</span>
      </div>
      <DevMark />
    </header>
  )
}

/** The add button for a tab: a circle floating above the tab bar, in the corner
 * the thumb already rests in. It sits over the page rather than in the header
 * because reaching the top of a phone one-handed is the whole problem. */
export function Fab({ to, label }: { to: string; label: string }) {
  return (
    <Link className="fab" to={to} aria-label={label} title={label}>
      <svg viewBox="0 0 24 24" aria-hidden="true" {...stroke} strokeWidth={2.2}>
        <path d="M12 5v14M5 12h14" />
      </svg>
    </Link>
  )
}

/** The body of a tab root. Its header belongs to the shell, so a tab supplies
 * content only. */
export function TabPage({ children }: { children: ReactNode }) {
  return <main className="main">{children}</main>
}

/** Page gives a pushed screen — a host, an app, a form — its own sticky header:
 * the way back, what you tapped into, and its action. */
export function Page({
  title,
  back,
  action,
  children,
}: {
  title: string
  /** Path to go back to; omitted where there is nothing to go back to. */
  back?: string
  action?: ReactNode
  children: ReactNode
}) {
  const navigate = useNavigate()
  return (
    <>
      <header className="header">
        {back && (
          <button className="back" onClick={() => navigate(back)} aria-label="Back">
            ‹ Back
          </button>
        )}
        <h1>{title}</h1>
        {action}
      </header>
      <main className="main">{children}</main>
    </>
  )
}

/** The developer mark at the end of the shared header: a small dark disk that
 * throws the full badge over the app for a moment when it is tapped.
 * Deliberately a button rather than a link — it goes nowhere. */
function DevMark() {
  const [flash, setFlash] = useState(false)

  useEffect(() => {
    if (!flash) return

    const timer = window.setTimeout(() => setFlash(false), DEV_FLASH_MS)
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setFlash(false)
    }

    window.addEventListener('keydown', onKey)
    return () => {
      window.clearTimeout(timer)
      window.removeEventListener('keydown', onKey)
    }
  }, [flash])

  return (
    <>
      <button
        type="button"
        className="dev"
        title="Built by CM Hegday · 0x434d"
        aria-label="Show the developer badge"
        onClick={() => setFlash(true)}
      >
        <img className="dev-logo" src="/dev-badge.png" alt="" aria-hidden="true" />
      </button>

      {/* Portalled to the body on purpose. The header carries a backdrop-filter
          of its own, and an element with one becomes the backdrop root for its
          descendants and the containing block for their fixed positioning — so
          nested here the overlay blurred nothing and grew out of the header
          instead of the middle of the screen. At the body it covers the
          viewport, blurs the app behind it, and expands from the centre. */}
      {flash &&
        createPortal(
          <div className="dev-flash" role="presentation" onClick={() => setFlash(false)}>
            {/* Text rather than a link: the overlay clears itself after three
                seconds, so anything you could tap here would be a trap. */}
            <div className="dev-flash-lockup">
              <img
                className="dev-flash-logo"
                src="/dev-badge-full.png"
                alt="Built by CM Hegday — 0x434d"
              />
              <span className="dev-flash-handle">github.com/chinmay28</span>
            </div>
          </div>,
          document.body,
        )}
    </>
  )
}

export function TabBar() {
  return (
    <nav className="tabbar" aria-label="Sections">
      <NavLink to="/" end>
        <HomeIcon />
        Overview
      </NavLink>
      <NavLink to="/hosts">
        <ServerIcon />
        Hosts
      </NavLink>
      <NavLink to="/apps">
        <AppsIcon />
        Apps
      </NavLink>
      <NavLink to="/settings">
        <GearIcon />
        Settings
      </NavLink>
    </nav>
  )
}

const stroke = {
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 1.8,
  strokeLinecap: 'round' as const,
  strokeLinejoin: 'round' as const,
}

function HomeIcon() {
  return (
    <svg viewBox="0 0 24 24" {...stroke}>
      <path d="M3 10.5 12 3l9 7.5" />
      <path d="M5.5 9.5V20h13V9.5" />
    </svg>
  )
}

function ServerIcon() {
  return (
    <svg viewBox="0 0 24 24" {...stroke}>
      <rect x="3" y="4" width="18" height="7" rx="2" />
      <rect x="3" y="13" width="18" height="7" rx="2" />
      <path d="M7 7.5h.01M7 16.5h.01" />
    </svg>
  )
}

function AppsIcon() {
  return (
    <svg viewBox="0 0 24 24" {...stroke}>
      <path d="M12 3 21 7.5v9L12 21l-9-4.5v-9z" />
      <path d="M3 7.5 12 12l9-4.5M12 12v9" />
    </svg>
  )
}

function GearIcon() {
  return (
    <svg viewBox="0 0 24 24" {...stroke}>
      <circle cx="12" cy="12" r="3.2" />
      <path d="M19.4 15a1.6 1.6 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.6 1.6 0 0 0-1.8-.3 1.6 1.6 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1A1.6 1.6 0 0 0 9 19.4a1.6 1.6 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.6 1.6 0 0 0 .3-1.8 1.6 1.6 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1A1.6 1.6 0 0 0 4.6 9a1.6 1.6 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.6 1.6 0 0 0 1.8.3H9a1.6 1.6 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.6 1.6 0 0 0 1 1.5 1.6 1.6 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.6 1.6 0 0 0-.3 1.8V9a1.6 1.6 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.6 1.6 0 0 0-1.5 1z" />
    </svg>
  )
}
