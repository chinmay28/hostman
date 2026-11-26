import { useState } from 'react'
import { powerChoices, wakeChoice, type PowerAction, type PowerChoice, type PowerHost } from '../lib/power'
import { Banner, Card, Sheet } from './ui'

/** Reboot, shut down and sleep for one host. A press only ever opens the
 *  question; nothing is asked of the host until that is answered, so a thumb
 *  landing on the wrong button costs a tap on Cancel and not a machine.
 *
 *  Wake is the way back from two of them, and appears beneath them once the
 *  host has stopped answering. It asks nothing first: a machine woken by
 *  mistake is an inconvenience, not a loss.
 *
 *  `onConfirm` and `onWake` do the asking and settle either way. Saying how it
 *  went is the caller's, which owns the page's banners. */
export function PowerCard({
  host,
  username,
  sudoOk,
  onConfirm,
  onWake,
}: {
  host: PowerHost
  username: string
  sudoOk: boolean
  onConfirm: (choice: PowerChoice) => Promise<void>
  onWake: () => Promise<void>
}) {
  const [asking, setAsking] = useState<PowerAction | null>(null)
  const [working, setWorking] = useState(false)

  const choices = powerChoices(host)
  const asked = choices.find((c) => c.action === asking) ?? null
  const sleep = choices.find((c) => c.action === 'sleep')

  const wake = wakeChoice(host)
  const [waking, setWaking] = useState(false)
  const sendWake = async () => {
    setWaking(true)
    try {
      await onWake()
    } finally {
      setWaking(false)
    }
  }

  const confirm = async (choice: PowerChoice) => {
    setWorking(true)
    try {
      await onConfirm(choice)
    } finally {
      setWorking(false)
      setAsking(null)
    }
  }

  return (
    <>
      <Card>
        <div className="title">Power</div>
        <p className="sub" style={{ marginTop: 4 }}>
          {host.isSelf
            ? 'This is the machine HostMan runs on, so each of these takes HostMan with it. Only a reboot brings it back by itself.'
            : 'The host goes a few seconds after you confirm, and HostMan reports it offline until it answers again. Only a reboot brings it back by itself.'}
        </p>
        {!sudoOk && (
          <Banner tone="warn">
            {username} needs passwordless sudo here before HostMan can reboot, shut down or sleep the machine.
          </Banner>
        )}
        <div className="actions">
          {choices.map((c) => (
            <button
              key={c.action}
              className="secondary"
              onClick={() => setAsking(c.action)}
              disabled={c.unavailable !== null}
              title={c.unavailable ?? undefined}
              data-power={c.action}
            >
              {c.label}
            </button>
          ))}
        </div>
        {/* A disabled button cannot say why on a phone, where there is no hover. */}
        {host.status === 'online' && sleep?.unavailable && (
          <p className="sub" style={{ marginTop: 10 }}>
            Sleep is off: {sleep.unavailable}
          </p>
        )}
        {wake.offered && (
          <>
            <button
              className="primary block"
              style={{ marginTop: 10 }}
              onClick={sendWake}
              disabled={waking || wake.unavailable !== null}
              data-power="wake"
            >
              {waking ? 'Sending…' : 'Wake'}
            </button>
            <p className="sub" style={{ marginTop: 10 }}>
              {wake.unavailable ??
                'Sends a wake-on-LAN packet. It reaches a machine on the same network as HostMan, with a wired card and wake-on-LAN enabled in its firmware.'}
            </p>
          </>
        )}
      </Card>
      {asked && (
        <Sheet
          title={asked.confirmTitle}
          subtitle={asked.confirmBody}
          onClose={() => !working && setAsking(null)}
        >
          {host.isSelf && <Banner tone="warn">{asked.selfWarning}</Banner>}
          <div className="actions">
            <button className="secondary" onClick={() => setAsking(null)} disabled={working}>
              Cancel
            </button>
            <button
              className="danger"
              onClick={() => confirm(asked)}
              disabled={working}
              data-power-confirm={asked.action}
            >
              {working ? asked.busy : asked.label}
            </button>
          </div>
        </Sheet>
      )}
    </>
  )
}
