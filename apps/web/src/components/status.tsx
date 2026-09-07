import { Badge } from './ui'
import { serviceName } from '../lib/format'
import type {
  BootCause,
  BootConfidence,
  DeploymentStatus,
  HealthStatus,
  HostStatus,
  ServiceUnit,
} from '../types'

export function HostBadge({ status }: { status: HostStatus }) {
  switch (status) {
    case 'online':
      return (
        <Badge tone="good" dot>
          Online
        </Badge>
      )
    case 'offline':
      return (
        <Badge tone="bad" dot>
          Offline
        </Badge>
      )
    case 'error':
      return (
        <Badge tone="bad" dot>
          Needs attention
        </Badge>
      )
    default:
      return (
        <Badge tone="neutral" dot>
          Not checked
        </Badge>
      )
  }
}

/** HomeBadge marks the machine HostMan itself is running on. */
export function HomeBadge() {
  return <Badge tone="accent">Home</Badge>
}

/** What a service is doing now. A one-shot unit that ran and exited is not
 *  running and has not failed either — calling that "stopped" would read as a
 *  problem, so it gets its own word. An active timer is not running anything
 *  either: it is armed and counting, which is the same good news said properly. */
export function ServiceBadge({ unit }: { unit: ServiceUnit }) {
  switch (unit.active) {
    case 'active':
      if (unit.timer) {
        return (
          <Badge tone="good" dot>
            Waiting
          </Badge>
        )
      }
      return unit.sub === 'exited' ? (
        <Badge tone="neutral" dot>
          Finished
        </Badge>
      ) : (
        <Badge tone="good" dot>
          Running
        </Badge>
      )
    case 'failed':
      return (
        <Badge tone="bad" dot>
          Failed
        </Badge>
      )
    case 'activating':
      return (
        <Badge tone="accent" dot pulse>
          Starting
        </Badge>
      )
    case 'deactivating':
      return (
        <Badge tone="warn" dot pulse>
          Stopping
        </Badge>
      )
    case 'reloading':
      return (
        <Badge tone="accent" dot pulse>
          Reloading
        </Badge>
      )
    default:
      return (
        <Badge tone="neutral" dot>
          Stopped
        </Badge>
      )
  }
}

/** Whether the service comes back on its own after a reboot — the question
 *  "is it running?" does not answer, and the one people forget. */
export function BootBadge({ unit }: { unit: ServiceUnit }) {
  switch (unit.fileState) {
    case 'enabled':
      return <Badge tone="accent">Starts at boot</Badge>
    case 'enabled-runtime':
      return <Badge tone="warn">Until reboot</Badge>
    case 'disabled':
      return <Badge tone="neutral">Manual start</Badge>
    case 'static':
      // "another unit" is the honest answer only until systemd is asked which
      // one. Where there is a single one, the badge says its name; where
      // several pull it in, no name is the whole truth and the card below
      // lists them all.
      return (
        <Badge tone="neutral">
          {unit.startedBy?.length === 1
            ? `Started by ${serviceName(unit.startedBy[0])}`
            : 'Started by another unit'}
        </Badge>
      )
    case 'masked':
      return <Badge tone="warn">Masked</Badge>
    default:
      return null
  }
}

/** What took the machine down last, in two words. A restart something asked for
 *  is good news and reads as such; an unexplained one is deliberately neutral
 *  rather than alarming, because "HostMan could not tell" is not a fault
 *  found. */
const CAUSE_WORD: Record<BootCause, { word: string; tone: 'good' | 'warn' | 'bad' | 'neutral' }> = {
  clean: { word: 'Asked for', tone: 'good' },
  panic: { word: 'Kernel panic', tone: 'bad' },
  lockup: { word: 'Locked up', tone: 'bad' },
  oom: { word: 'Out of memory', tone: 'warn' },
  overheat: { word: 'Overheated', tone: 'bad' },
  undervoltage: { word: 'Under-voltage', tone: 'warn' },
  power: { word: 'Lost power', tone: 'warn' },
  storage: { word: 'Storage', tone: 'bad' },
  unknown: { word: 'Unexplained', tone: 'neutral' },
}

export function CauseBadge({ cause }: { cause: BootCause }) {
  const { word, tone } = CAUSE_WORD[cause] ?? CAUSE_WORD.unknown
  return <Badge tone={tone}>{word}</Badge>
}

/** How far the verdict above should be trusted. It is a separate badge because
 *  the two are separate claims: "it panicked" and "HostMan is sure of it" can
 *  come apart, and a screen that ran them together would be overstating a
 *  guess. */
export function ConfidenceBadge({ confidence }: { confidence: BootConfidence }) {
  switch (confidence) {
    case 'certain':
      return <Badge tone="neutral">The machine said so</Badge>
    case 'likely':
      return <Badge tone="neutral">Best explanation</Badge>
    default:
      return <Badge tone="neutral">Not enough to say</Badge>
  }
}

export function HealthBadge({ status }: { status: HealthStatus }) {
  switch (status) {
    case 'passing':
      return <Badge tone="good">Healthy</Badge>
    case 'failing':
      return <Badge tone="bad">Not responding</Badge>
    case 'unchecked':
      return <Badge tone="neutral">No health check</Badge>
    default:
      return <Badge tone="neutral">Unknown</Badge>
  }
}

export function DeploymentBadge({ status }: { status: DeploymentStatus }) {
  switch (status) {
    case 'running':
      return (
        <Badge tone="accent" dot pulse>
          Running
        </Badge>
      )
    case 'succeeded':
      return <Badge tone="good">Succeeded</Badge>
    case 'failed':
      return <Badge tone="bad">Failed</Badge>
    case 'canceled':
      return <Badge tone="warn">Canceled</Badge>
    case 'interrupted':
      return <Badge tone="warn">Interrupted</Badge>
  }
}
