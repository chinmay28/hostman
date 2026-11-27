import type { Host } from '../types'

/** Everything HostMan will do to a host's power. The server refuses anything
 *  else before it reaches the machine. */
export type PowerAction = 'reboot' | 'shutdown' | 'sleep'

/** What the power card needs to know about a host. */
export type PowerHost = Pick<Host, 'name' | 'status' | 'isSelf' | 'canSleep' | 'wakeMac'>

/** One button on the power card, and the question asked before it does
 *  anything. Every action is confirmed: each takes the machine away, and two
 *  of the three need someone to go and bring it back. */
export interface PowerChoice {
  action: PowerAction
  label: string
  /** The button's wording while the host is being asked. */
  busy: string
  /** Why the button cannot be pressed, or null where it can. */
  unavailable: string | null
  confirmTitle: string
  confirmBody: string
  /** Shown above the buttons where the machine is the one HostMan runs on. */
  selfWarning: string
  /** What the screen says once the host has accepted. */
  done: string
}

const wording: Record<PowerAction, Omit<PowerChoice, 'action' | 'unavailable'>> = {
  reboot: {
    label: 'Reboot',
    busy: 'Rebooting…',
    confirmTitle: 'Reboot {name}?',
    confirmBody: 'Everything running on the machine stops and starts again.',
    selfWarning: 'HostMan runs on this machine. It goes down with it, and comes back when the machine does.',
    done: '{name} is rebooting. It will show as offline until it answers again.',
  },
  shutdown: {
    label: 'Shut down',
    busy: 'Shutting down…',
    confirmTitle: 'Shut down {name}?',
    confirmBody: 'Everything running on the machine stops, and it stays off. {back}',
    selfWarning:
      'HostMan runs on this machine. It goes down with it, and stays down until the machine is turned on again.',
    done: '{name} is shutting down. It stays offline until someone turns it on.',
  },
  sleep: {
    label: 'Sleep',
    busy: 'Putting to sleep…',
    confirmTitle: 'Put {name} to sleep?',
    confirmBody: 'What is running is kept in memory but stops answering. {back}',
    selfWarning: 'HostMan runs on this machine. It stops answering until the machine is woken.',
    done: '{name} is going to sleep. It stays offline until something wakes it.',
  },
}

/** What brings a machine back from off or asleep, which is the thing to know
 *  before sending it there. */
function wayBack(host: PowerHost): string {
  return canWake(host)
    ? 'HostMan can try to wake it over the network afterwards, which works where wake-on-LAN is enabled in its firmware. Failing that, it takes its power button.'
    : 'HostMan cannot bring it back: that takes its power button.'
}

/** Whether there is anything to send a wake packet to. */
const canWake = (host: PowerHost) => !host.isSelf && host.wakeMac !== ''

const ORDER: PowerAction[] = ['reboot', 'shutdown', 'sleep']

/** The power card's buttons for a host, in the order they are shown. A host
 *  that is not online can be asked nothing; one whose kernel offers no suspend
 *  state — a Raspberry Pi, where an x86 PC has one — keeps its Sleep button,
 *  disabled, with the reason. */
export function powerChoices(host: PowerHost): PowerChoice[] {
  return ORDER.map((action) => {
    const w = wording[action]
    const named = (s: string) => s.replace('{name}', host.name)
    let unavailable: string | null = null
    if (host.status !== 'online') unavailable = `${host.name} is not online.`
    else if (action === 'sleep' && !host.canSleep) unavailable = `${host.name} has no sleep state to enter.`
    return {
      ...w,
      action,
      unavailable,
      confirmTitle: named(w.confirmTitle),
      confirmBody: w.confirmBody.replace('{back}', wayBack(host)),
      done: named(w.done),
    }
  })
}

/** What the screen says once a host has accepted a power action. After a
 *  shutdown or a sleep that includes whether its network card was left
 *  listening, since that decides whether Wake is worth pressing. */
export function powerDone(choice: PowerChoice, host: PowerHost, wakeArmed: boolean): string {
  if (choice.action === 'reboot' || !canWake(host)) return choice.done
  return wakeArmed
    ? `${choice.done} Wake-on-LAN is switched on, so Wake can bring it back.`
    : `${choice.done} Wake-on-LAN could not be switched on there, so Wake may not work.`
}

/** The Wake button: shown for a host that is not answering, since waking one
 *  that is awake means nothing. */
export interface WakeChoice {
  /** Whether the button belongs on the screen at all right now. */
  offered: boolean
  /** Why it cannot be pressed, or null where it can. */
  unavailable: string | null
  /** What the screen says once the packet has gone. The server repeats it
   *  for a minute, so a press that beat the machine to sleep still lands. */
  done: string
}

export function wakeChoice(host: PowerHost): WakeChoice {
  let unavailable: string | null = null
  if (host.isSelf) unavailable = `HostMan runs on ${host.name}, so it cannot wake it.`
  else if (host.wakeMac === '')
    unavailable = `HostMan has not seen a wired network card on ${host.name}, so it has nothing to send a wake packet to.`
  return {
    offered: host.status !== 'online',
    unavailable,
    done: `Wake packet sent to ${host.name}, and HostMan sends it again over the next minute in case the machine was still going down. If it is listening it will show as online soon after.`,
  }
}
