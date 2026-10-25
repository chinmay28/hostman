import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import { api } from '../api'
import { Page } from '../components/Layout'
import { Badge, Banner, Card, Copyable, Empty, Loading, Sheet, useLoader } from '../components/ui'
import { uptime } from '../lib/format'
import type { VM, VMAction } from '../types'

/** How to get VirtualBox onto a Debian or Ubuntu host. HostMan will not do it:
 *  a hypervisor is a decision about what a machine is for, and it comes with
 *  kernel modules the host's own package manager should build. */
const INSTALL = 'sudo apt install -y virtualbox'

/** How often to ask while a machine is between states. Every other host screen
 *  answers when asked, because each answer is an SSH session — but a machine
 *  that is starting will be running in a few seconds, and a screen that made
 *  you tap to find out would be worse company than one that looks for you. */
const BUSY_POLL_MS = 3000

/** The states that mean VirtualBox is in the middle of something. */
const MOVING = ['starting', 'stopping', 'saving', 'restoring', 'livesnapshotting', 'teleporting']

/** The two verbs that do not ask the guest anything. Reset is a power cycle
 *  and poweroff pulls the plug; whatever the guest had not written is gone. */
const CONFIRM: VMAction[] = ['poweroff', 'reset']

/**
 * Virtual machines: VirtualBox on the host, driven from the phone.
 *
 * The screen manages what is there. Each machine is a card with VirtualBox's
 * own word for its state and the verbs that make sense from it — a stopped
 * machine starts, a running one pauses, saves or shuts down, a paused one
 * resumes. Making or deleting a machine is a laptop job and is not offered.
 */
export default function HostVMs() {
  const { id } = useParams()
  const hostId = Number(id)

  const { data: host } = useLoader(() => api.host(hostId), [hostId])
  const [pace, setPace] = useState<number | undefined>(undefined)
  const { data: list, error, offline, loading, reload } = useLoader(
    () => api.vms(hostId),
    [hostId],
    pace,
  )

  const [busy, setBusy] = useState<string | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  const [confirming, setConfirming] = useState<{ vm: VM; action: VMAction } | null>(null)

  const moving = (list?.vms ?? []).some((vm) => MOVING.includes(vm.state))
  useEffect(() => {
    setPace(moving ? BUSY_POLL_MS : undefined)
  }, [moving])

  const act = async (vm: VM, action: VMAction) => {
    setBusy(`${vm.id}:${action}`)
    setFailure(null)
    try {
      await api.vmAction(hostId, action, vm.id)
      setConfirming(null)
      reload()
    } catch (e) {
      setFailure(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(null)
    }
  }

  // The verbs that do not ask the guest first ask you instead.
  const request = (vm: VM, action: VMAction) => {
    if (CONFIRM.includes(action)) setConfirming({ vm, action })
    else act(vm, action)
  }

  return (
    <Page
      title="Virtual machines"
      back={`/hosts/${hostId}`}
      action={
        <button className="ghost" onClick={reload} disabled={loading}>
          Refresh
        </button>
      }
    >
      <Loading error={error} offline={offline} hasData={!!list} />
      {failure && <Banner tone="bad">{failure}</Banner>}

      {!list && !error && (
        <Card>
          <div className="sub">Asking {host?.name ?? 'the host'} about VirtualBox…</div>
        </Card>
      )}

      {list && !list.installed && <NotInstalled hostName={host?.name} />}

      {list && list.installed && (
        <>
          {list.vms.length === 0 ? (
            <Empty
              message={`${list.user} has no virtual machines on ${host?.name ?? 'this host'}. Machines made as another user, or as root, are theirs and show up for them.`}
            />
          ) : (
            list.vms.map((vm) => (
              <VMCard
                key={vm.id}
                vm={vm}
                busy={busy?.startsWith(vm.id + ':') ? busy.slice(vm.id.length + 1) : null}
                anyBusy={busy !== null}
                onAction={(action) => request(vm, action)}
              />
            ))
          )}

          <Card>
            <div className="title">Whose machines these are</div>
            <p className="sub" style={{ marginTop: 4 }}>
              VirtualBox keeps a registry per user, so this is what {list.user} sees on{' '}
              {host?.name ?? 'the host'}
              {list.version ? ` with VirtualBox ${list.version}` : ''}. Starting a machine from here
              is always headless: there is no display at the other end of an SSH session.
            </p>
          </Card>
        </>
      )}

      {confirming && (
        <Sheet
          title={confirming.action === 'reset' ? `Reset ${confirming.vm.name}?` : `Power off ${confirming.vm.name}?`}
          subtitle={
            confirming.action === 'reset'
              ? 'A power cycle: the guest is not asked, and whatever it had not written to disk is lost.'
              : 'The plug is pulled: the guest is not asked, and whatever it had not written to disk is lost. Shut down asks it instead.'
          }
          onClose={() => setConfirming(null)}
        >
          <button
            className="danger block"
            onClick={() => act(confirming.vm, confirming.action)}
            disabled={busy !== null}
          >
            {busy ? 'Working…' : confirming.action === 'reset' ? 'Reset it' : 'Power it off'}
          </button>
          <button className="secondary block" onClick={() => setConfirming(null)}>
            Keep it running
          </button>
        </Sheet>
      )}
    </Page>
  )
}

/** VirtualBox is the one thing on this screen HostMan will not install, so
 *  this is the whole of the screen until somebody has. The host page would not
 *  normally lead here — the card appears only where the probe found it — so
 *  this is what a bookmark finds after VirtualBox was removed. */
function NotInstalled({ hostName }: { hostName?: string }) {
  return (
    <Card>
      <div className="title">VirtualBox is not installed here</div>
      <p className="sub" style={{ marginTop: 4 }}>
        HostMan found no <code>VBoxManage</code> on {hostName ?? 'this host'}. It will not install
        a hypervisor for you — that is a decision about what the machine is for, and it comes with
        kernel modules the host's own packages should build. On Debian or Ubuntu:
      </p>
      <Copyable text={INSTALL} />
      <p className="sub">The card on the host's page appears by itself once it is there.</p>
    </Card>
  )
}

/** One machine: its name, its state, what it was given, and the verbs that
 *  make sense from where it is. The verbs are deliberately few — the ones a
 *  person reaches for a phone to do — and the two that can lose the guest's
 *  work sit behind a confirmation. */
function VMCard({
  vm,
  busy,
  anyBusy,
  onAction,
}: {
  vm: VM
  /** The action running on this machine, if one is. */
  busy: string | null
  anyBusy: boolean
  onAction: (action: VMAction) => void
}) {
  const verbs = actionsFor(vm.state)
  const label = (action: VMAction) =>
    busy === action ? `${WORKING[action]}…` : LABELS[action]
  return (
    <Card>
      <div className="row between">
        <div className="grow">
          <div className="title">{vm.name}</div>
          <div className="sub">{describe(vm)}</div>
        </div>
        <StateBadge state={vm.state} />
      </div>
      {verbs.length > 0 && (
        <div className="row" style={{ marginTop: 10, flexWrap: 'wrap', gap: 8 }}>
          {verbs.map((action) => (
            <button
              key={action}
              className={CONFIRM.includes(action) ? 'danger' : action === 'start' || action === 'resume' ? '' : 'secondary'}
              onClick={() => onAction(action)}
              disabled={anyBusy}
            >
              {label(action)}
            </button>
          ))}
        </div>
      )}
    </Card>
  )
}

const LABELS: Record<VMAction, string> = {
  start: 'Start',
  stop: 'Shut down',
  poweroff: 'Power off',
  reset: 'Reset',
  pause: 'Pause',
  resume: 'Resume',
  save: 'Save state',
}

const WORKING: Record<VMAction, string> = {
  start: 'Starting',
  stop: 'Shutting down',
  poweroff: 'Powering off',
  reset: 'Resetting',
  pause: 'Pausing',
  resume: 'Resuming',
  save: 'Saving',
}

/** Which verbs a state takes. A saved machine starts (VirtualBox restores it);
 *  a paused one resumes or is powered off; a running one does everything else.
 *  A machine between states, or one VirtualBox cannot open, takes nothing. */
export function actionsFor(state: string): VMAction[] {
  switch (state) {
    case 'poweroff':
    case 'aborted':
    case 'saved':
      return ['start']
    case 'running':
      return ['pause', 'save', 'stop', 'poweroff', 'reset']
    case 'paused':
      return ['resume', 'poweroff']
    default:
      return []
  }
}

/** The badge reads the state the way the host's own does: green is up, amber
 *  is somewhere in between, red is something to look at, grey is off. */
function StateBadge({ state }: { state: string }) {
  if (MOVING.includes(state)) return <Badge tone="accent" dot pulse>{state}</Badge>
  switch (state) {
    case 'running':
      return <Badge tone="good" dot>running</Badge>
    case 'paused':
    case 'saved':
      return <Badge tone="warn" dot>{state}</Badge>
    case 'aborted':
    case 'inaccessible':
      return <Badge tone="bad" dot>{state}</Badge>
    case 'poweroff':
      return <Badge tone="neutral">off</Badge>
    default:
      return <Badge tone="neutral">{state}</Badge>
  }
}

/** The subtitle answers "what is it, and for how long?": the guest it was
 *  told it runs, what it was given, and how long it has been in this state. */
export function describe(vm: VM, now = Date.now()): string {
  const parts: string[] = []
  if (vm.osType) parts.push(vm.osType.replace(/_64$/, ' (64-bit)').replace(/_/g, ' '))
  if (vm.cpus) parts.push(`${vm.cpus} CPU${vm.cpus === 1 ? '' : 's'}`)
  if (vm.memoryMb) parts.push(memory(vm.memoryMb))
  if (vm.since) {
    const seconds = Math.max(0, Math.round((now - new Date(vm.since).getTime()) / 1000))
    if (seconds >= 60) parts.push(`${vm.state === 'running' ? 'up' : vm.state} ${uptime(seconds)}`)
  }
  if (vm.state === 'inaccessible') parts.push('VirtualBox cannot open its files')
  return parts.join(' · ') || vm.id
}

/** Memory as VirtualBox hands it out: in MB, and in whole GB where it is one. */
function memory(mb: number): string {
  if (mb >= 1024 && mb % 1024 === 0) return `${mb / 1024} GB`
  if (mb >= 1024) return `${(mb / 1024).toFixed(1)} GB`
  return `${mb} MB`
}
