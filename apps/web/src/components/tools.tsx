import type { MouseEvent } from 'react'
import { useNavigate } from 'react-router-dom'

/** The four errands a host is opened for, in the order the host's own page
 *  lists them. Everything else the page offers — the remote browser, torrents,
 *  why it restarted — is a longer visit and stays a tap further in. */
const TOOLS = [
  { path: 'files', label: 'Files', icon: <FilesIcon /> },
  { path: 'services', label: 'Services', icon: <ServicesIcon /> },
  { path: 'shell', label: 'Terminal', icon: <TerminalIcon /> },
  { path: 'cron', label: 'Jobs', icon: <ClockIcon /> },
] as const

/**
 * HostTools is the row of shortcuts on a host's card: straight to its files,
 * services, terminal and scheduled jobs, skipping the host page. The card is a
 * link to the host, so each chip takes its own tap the way AppRow does. Off a
 * host HostMan cannot reach, the chips dim and go nowhere — a terminal to an
 * offline machine is a screen that says so, and the card already has.
 */
export function HostTools({ hostId, enabled }: { hostId: number; enabled: boolean }) {
  const navigate = useNavigate()
  const open = (path: string) => (e: MouseEvent) => {
    e.preventDefault()
    e.stopPropagation()
    if (enabled) navigate(`/hosts/${hostId}/${path}`)
  }
  return (
    <div className="tools" role="group" aria-label="Open on this host">
      {TOOLS.map((tool) => (
        <button
          key={tool.path}
          type="button"
          className="tool"
          onClick={open(tool.path)}
          disabled={!enabled}
          aria-label={tool.label}
        >
          {tool.icon}
          <span>{tool.label}</span>
        </button>
      ))}
    </div>
  )
}

const stroke = {
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 1.8,
  strokeLinecap: 'round' as const,
  strokeLinejoin: 'round' as const,
}

function FilesIcon() {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true" {...stroke}>
      <path d="M3 6.5A1.5 1.5 0 0 1 4.5 5h4l2 2h9A1.5 1.5 0 0 1 21 8.5v9a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 17.5z" />
    </svg>
  )
}

function ServicesIcon() {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true" {...stroke}>
      <circle cx="12" cy="12" r="8" />
      <path d="M10 9.5v5l4-2.5z" />
    </svg>
  )
}

function TerminalIcon() {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true" {...stroke}>
      <path d="M5 8l4 4-4 4" />
      <path d="M12 16h7" />
    </svg>
  )
}

function ClockIcon() {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true" {...stroke}>
      <circle cx="12" cy="12" r="8" />
      <path d="M12 8v4l2.5 2" />
    </svg>
  )
}
