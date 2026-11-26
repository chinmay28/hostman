import type { Host } from '../types'

/** Everything HostMan will do to a host's power. The server refuses anything
 *  else before it reaches the machine. */
export type PowerAction = 'reboot' | 'shutdown' | 'sleep'

/** What the power card needs to know about a host. */
export type PowerHost = Pick<Host, 'name' | 'status' | 'isSelf' | 'canSleep'>

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
    confirmBody:
      'Everything running on the machine stops, and it stays off. HostMan cannot turn it back on: someone has to press its power button.',
    selfWarning:
      'HostMan runs on this machine. It goes down with it, and stays down until the machine is turned on again.',
    done: '{name} is shutting down. It stays offline until someone turns it on.',
  },
  sleep: {
    label: 'Sleep',
    busy: 'Putting to sleep…',
    confirmTitle: 'Put {name} to sleep?',
    confirmBody:
      'What is running is kept in memory but stops answering. HostMan cannot wake it: that takes a key press, its power button or a wake-on-LAN packet.',
    selfWarning: 'HostMan runs on this machine. It stops answering until the machine is woken.',
    done: '{name} is going to sleep. It stays offline until something wakes it.',
  },
}

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
      done: named(w.done),
    }
  })
}
