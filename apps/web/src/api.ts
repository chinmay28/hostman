import type {
  App,
  BootReport,
  ClaudeHost,
  ClaudeSession,
  Crontab,
  Deployment,
  DirListing,
  DirUsage,
  Host,
  HostFile,
  HostProcesses,
  HostTestResult,
  Installation,
  JournalStorage,
  MetricSummary,
  Overview,
  ProvisionResult,
  RemoteSession,
  Sample,
  SelfInfo,
  ServiceAction,
  ServiceActionResult,
  ServiceList,
  ServiceLog,
  ServiceUnit,
  ShellSession,
  SSHKeyInfo,
  TorrentDaemon,
} from './types'

/** ApiError carries the server's message so the UI can show it verbatim. */
export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let response: Response
  try {
    response = await fetch(path, {
      ...init,
      headers: init?.body ? { 'Content-Type': 'application/json', ...init?.headers } : init?.headers,
    })
  } catch {
    throw new ApiError(0, 'Could not reach HostMan. Is it still running?')
  }
  if (response.status === 204) return undefined as T
  const text = await response.text()
  const body = text ? JSON.parse(text) : undefined
  if (!response.ok) {
    throw new ApiError(response.status, body?.error ?? `Request failed (${response.status})`)
  }
  return body as T
}

const json = (body: unknown): RequestInit => ({ body: JSON.stringify(body) })

export interface HostInput {
  name: string
  address: string
  port: number
  username: string
}

export interface AppInput {
  name: string
  description: string
  installCommand: string
  uninstallCommand: string
  params: App['params']
  healthType: App['healthType']
  healthTarget: string
}

export const api = {
  overview: () => request<Overview>('/api/overview'),

  hosts: () => request<Host[]>('/api/hosts'),
  host: (id: number) => request<Host>(`/api/hosts/${id}`),
  createHost: (input: HostInput) => request<Host>('/api/hosts', { method: 'POST', ...json(input) }),
  updateHost: (id: number, input: Partial<HostInput>) =>
    request<Host>(`/api/hosts/${id}`, { method: 'PATCH', ...json(input) }),
  deleteHost: (id: number) => request<void>(`/api/hosts/${id}`, { method: 'DELETE' }),
  testHost: (id: number) => request<HostTestResult>(`/api/hosts/${id}/test`, { method: 'POST' }),
  /** One-time setup. The password is sent, used and forgotten — never stored. */
  provisionHost: (id: number, password: string) =>
    request<ProvisionResult>(`/api/hosts/${id}/provision`, { method: 'POST', ...json({ password }) }),
  /** Samples cover the window asked for; the summary always covers the last 24
   *  hours, which is everything the server keeps. The process snapshot comes
   *  from the newest probe — the same round trip that took the sample, so
   *  seeing what a host is busy with costs no extra SSH session. */
  hostMetrics: (id: number, minutes = 60) =>
    request<{
      hostId: number
      minutes: number
      samples: Sample[]
      summary: MetricSummary
      processes: HostProcesses | null
    }>(`/api/hosts/${id}/metrics?minutes=${minutes}`),
  /** Restart the machine. It goes down a few seconds after this returns.
   *  There is no shutdown: HostMan cannot turn a host back on. */
  reboot: (id: number) =>
    request<{ status: string }>(`/api/hosts/${id}/reboot`, { method: 'POST' }),

  /** Why the machine restarted last: HostMan's best guess and the evidence
   *  behind it. One SSH session that reads wtmp, the previous boot's log and
   *  the Pi firmware's throttle flags, so it is asked for on demand and never
   *  polled. */
  boot: (id: number) => request<BootReport>(`/api/hosts/${id}/boot`),
  /** Make the host's journal survive a restart, which is what turns "there is
   *  no record of it" into an answer next time. Idempotent. */
  keepJournal: (id: number) =>
    request<JournalStorage>(`/api/hosts/${id}/boot/journal`, { method: 'POST' }),

  /** The browser running on the host: what is installed, how far a setup got,
   *  whether it is up, and what has been downloaded. Nothing here writes, so
   *  the screen watching an install is free to ask again on a timer. */
  /** Claude Code on a host: installed, signed in, or on the way to either. */
  claude: (id: number) => request<ClaudeHost>(`/api/hosts/${id}/claude`),
  installClaude: (id: number) =>
    request<ClaudeHost>(`/api/hosts/${id}/claude/install`, { method: 'POST' }),
  /** Starts a sign-in on the host. The link to open comes back in the status
   *  once the CLI has printed it. */
  claudeLogin: (id: number, console = false) =>
    request<ClaudeHost>(`/api/hosts/${id}/claude/login`, { method: 'POST', ...json({ console }) }),
  claudeLoginCode: (id: number, code: string) =>
    request<ClaudeHost>(`/api/hosts/${id}/claude/login/code`, { method: 'POST', ...json({ code }) }),
  cancelClaudeLogin: (id: number) =>
    request<ClaudeHost>(`/api/hosts/${id}/claude/login`, { method: 'DELETE' }),
  claudeKey: (id: number, key: string) =>
    request<ClaudeHost>(`/api/hosts/${id}/claude/key`, { method: 'POST', ...json({ key }) }),
  claudeSessions: (id: number) => request<ClaudeSession[]>(`/api/hosts/${id}/claude/sessions`),
  openClaude: (id: number, input: { dir: string; model: string; mode: string; name?: string }) =>
    request<ClaudeSession>(`/api/hosts/${id}/claude/sessions`, { method: 'POST', ...json(input) }),
  claudeSession: (sid: string) => request<ClaudeSession>(`/api/claude/${sid}`),
  closeClaude: (sid: string) => request<void>(`/api/claude/${sid}`, { method: 'DELETE' }),
  claudeSay: (sid: string, text: string) =>
    request<ClaudeSession>(`/api/claude/${sid}/message`, { method: 'POST', ...json({ text }) }),
  claudeAnswer: (sid: string, input: { requestId: string; allow: boolean; always?: boolean; reason?: string }) =>
    request<ClaudeSession>(`/api/claude/${sid}/answer`, { method: 'POST', ...json(input) }),
  claudeModel: (sid: string, model: string) =>
    request<ClaudeSession>(`/api/claude/${sid}/model`, { method: 'POST', ...json({ model }) }),
  claudeMode: (sid: string, mode: string) =>
    request<ClaudeSession>(`/api/claude/${sid}/mode`, { method: 'POST', ...json({ mode }) }),
  claudeInterrupt: (sid: string) =>
    request<ClaudeSession>(`/api/claude/${sid}/interrupt`, { method: 'POST' }),

  remote: (id: number) => request<RemoteSession>(`/api/hosts/${id}/remote`),
  /** Installs the session and starts the packages installing, which the host
   *  gets on with by itself. Idempotent, and it keeps the password and the
   *  browser profile: changing a screen size should not sign anybody out. */
  setupRemote: (
    id: number,
    input: { geometry?: string; port?: number; homepage?: string; reset?: boolean } = {},
  ) => request<RemoteSession>(`/api/hosts/${id}/remote`, { method: 'POST', ...json(input) }),
  /** Starts or stops it. A start carries the page to open, so opening a site
   *  is one round trip rather than a URL typed over VNC on a phone keyboard. */
  remoteAction: (id: number, action: 'start' | 'stop', url = '') =>
    request<RemoteSession>(`/api/hosts/${id}/remote/action`, {
      method: 'POST',
      ...json({ action, url }),
    }),
  /** Takes the session off the host. Purging takes the browser profile with it,
   *  and with it every site it was signed into. The downloads always stay. */
  removeRemote: (id: number, purge = false) =>
    request<void>(`/api/hosts/${id}/remote${purge ? '?purge=true' : ''}`, { method: 'DELETE' }),

  /** The downloader on a host: whether deluge is installed, what HostMan has
   *  set up, whether the daemon is running, and what it is downloading. It is
   *  a read, so a screen watching a progress bar is free to ask on a timer. */
  torrents: (id: number) => request<TorrentDaemon>(`/api/hosts/${id}/torrents`),
  /** Starts one torrent downloading: a magnet link, the address of a .torrent
   *  file for the host to fetch itself, or the file's own bytes when one was
   *  picked on the phone. A stopped daemon is started rather than refused. */
  addTorrent: (
    id: number,
    input: { source?: string; file?: string; name?: string; path?: string },
  ) => request<TorrentDaemon>(`/api/hosts/${id}/torrents`, { method: 'POST', ...json(input) }),
  /** Runs the daemon, or acts on one torrent. Removing takes the files with it
   *  only when asked — that is the one thing here that cannot be undone. */
  torrentAction: (
    id: number,
    action: 'start' | 'stop' | 'pause' | 'resume' | 'remove',
    torrentId = '',
    data = false,
  ) =>
    request<TorrentDaemon>(`/api/hosts/${id}/torrents/action`, {
      method: 'POST',
      ...json({ action, id: torrentId, data }),
    }),
  /** Tells deluge what to do with a torrent once it has finished: how much to
   *  upload before it stops seeding, and whether the entry then goes from the
   *  list. It is deluge's own setting, so it holds when nobody is looking —
   *  which is when torrents finish. A ratio of 0 means keep seeding. */
  torrentSeeding: (id: number, ratio: number, remove: boolean) =>
    request<TorrentDaemon>(`/api/hosts/${id}/torrents/action`, {
      method: 'POST',
      ...json({ action: 'seeding', ratio, remove }),
    }),
  /** Tells deluge how many torrents to work on at once — the rest wait as
   *  Queued until a slot opens. Deluge's own defaults are small, and a queue
   *  held under them reads as a stuck download. -1 means no limit at all. */
  torrentLimit: (id: number, limit: number) =>
    request<TorrentDaemon>(`/api/hosts/${id}/torrents/action`, {
      method: 'POST',
      ...json({ action: 'limit', limit }),
    }),
  /** Writes the daemon onto the host, or rewrites the one already there.
   *  Idempotent: the password and everything already downloading stay, so
   *  changing the folder does not restart a download. */
  setupTorrents: (id: number, input: { downloads?: string; reset?: boolean } = {}) =>
    request<TorrentDaemon>(`/api/hosts/${id}/torrents/setup`, { method: 'POST', ...json(input) }),
  /** Takes the daemon and deluge's state off the host. Deluge stays installed,
   *  and every file already downloaded stays where it is. */
  removeTorrents: (id: number) =>
    request<void>(`/api/hosts/${id}/torrents/setup`, { method: 'DELETE' }),

  /** The shells already open on a host, oldest first. A screen arriving on the
   *  terminal offers to rejoin one rather than opening another, which is what
   *  makes coming back to a half-typed command the normal case. */
  shells: (id: number) => request<ShellSession[]>(`/api/hosts/${id}/shell`),
  /** Opens a login shell. The size is what the screen measured; the server
   *  clamps it, so a bad measurement is a small terminal and not an error. */
  openShell: (id: number, cols: number, rows: number) =>
    request<ShellSession>(`/api/hosts/${id}/shell`, { method: 'POST', ...json({ cols, rows }) }),
  shell: (sid: string) => request<ShellSession>(`/api/shell/${sid}`),
  /** Keystrokes, base64. A terminal's most important keys are not text: Ctrl-C
   *  is one byte, an arrow key is three, and neither survives being a string. */
  shellInput: (sid: string, data: string) =>
    request<void>(`/api/shell/${sid}/input`, { method: 'POST', ...json({ data }) }),
  shellResize: (sid: string, cols: number, rows: number) =>
    request<ShellSession>(`/api/shell/${sid}/resize`, { method: 'POST', ...json({ cols, rows }) }),
  /** Ends the shell. Leaving the screen does not: that is the point of it
   *  living on the server. */
  closeShell: (sid: string) => request<void>(`/api/shell/${sid}`, { method: 'DELETE' }),

  /** An empty user means the account HostMan signs in as. */
  crontab: (id: number, user = '') =>
    request<Crontab>(`/api/hosts/${id}/cron?user=${encodeURIComponent(user)}`),
  saveCrontab: (id: number, user: string, content: string) =>
    request<Crontab>(`/api/hosts/${id}/cron`, { method: 'PUT', ...json({ user, content }) }),

  /** The services someone installed on this host by hand. Every one of these
   *  opens an SSH session, so they are asked for on demand, never polled. */
  services: (id: number) => request<ServiceList>(`/api/hosts/${id}/services`),
  service: (id: number, name: string) =>
    request<ServiceUnit>(`/api/hosts/${id}/services/unit?name=${encodeURIComponent(name)}`),
  serviceLog: (id: number, name: string, lines: number) =>
    request<ServiceLog>(
      `/api/hosts/${id}/services/logs?name=${encodeURIComponent(name)}&lines=${lines}`,
    ),
  /** Runs one systemctl verb. It returns once systemd has finished, which for
   *  a service that takes its time starting is a while. */
  serviceAction: (id: number, name: string, action: ServiceAction) =>
    request<ServiceActionResult>(`/api/hosts/${id}/services/action`, {
      method: 'POST',
      ...json({ name, action }),
    }),
  /** Writes a new unit file and hands it to systemd. The service is created
   *  stopped: starting it and enabling it are separate calls, so a unit that
   *  will not start is a service that exists and did not start. systemd
   *  validates the file, and anything it refuses to load is taken back off
   *  the disk rather than left there being wrong. */
  createService: (id: number, name: string, content: string) =>
    request<ServiceUnit>(`/api/hosts/${id}/services`, { method: 'POST', ...json({ name, content }) }),
  /** Deletes a service that is not running: its unit file, the symlinks
   *  enabling it, and its drop-in overrides. Whatever it ran is left alone. */
  deleteService: (id: number, name: string) =>
    request<void>(`/api/hosts/${id}/services?name=${encodeURIComponent(name)}`, { method: 'DELETE' }),

  /** daemon-reload: what turns an edited unit file into an edited service. */
  reloadServices: (id: number) =>
    request<{ status: string }>(`/api/hosts/${id}/services/reload`, { method: 'POST' }),

  /** An empty path lists the SSH user's home, which only the host knows. */
  files: (id: number, path = '') =>
    request<DirListing>(`/api/hosts/${id}/files?path=${encodeURIComponent(path)}`),
  file: (id: number, path: string) =>
    request<HostFile>(`/api/hosts/${id}/files/content?path=${encodeURIComponent(path)}`),
  /** Counts what is under a directory and how much disk it takes. Walks the
   *  whole tree on the host, so it can take a while on a big one. */
  usage: (id: number, path: string) =>
    request<DirUsage>(`/api/hosts/${id}/files/usage?path=${encodeURIComponent(path)}`),
  saveFile: (id: number, path: string, content: string) =>
    request<HostFile>(`/api/hosts/${id}/files/content`, { method: 'PUT', ...json({ path, content }) }),
  mkdir: (id: number, path: string) =>
    request<{ path: string }>(`/api/hosts/${id}/files/mkdir`, { method: 'POST', ...json({ path }) }),
  renameFile: (id: number, path: string, to: string) =>
    request<{ path: string }>(`/api/hosts/${id}/files/rename`, { method: 'POST', ...json({ path, to }) }),
  /** Sets permission bits, octal as the listing shows them. Recursive gives a
   *  directory and everything inside it the same mode. Answers with the mode
   *  the host reports afterwards. */
  chmod: (id: number, path: string, mode: string, recursive = false) =>
    request<{ path: string; mode: string }>(`/api/hosts/${id}/files/chmod`, {
      method: 'POST',
      ...json({ path, mode, recursive }),
    }),
  deleteFile: (id: number, path: string, recursive = false) =>
    request<void>(
      `/api/hosts/${id}/files?path=${encodeURIComponent(path)}${recursive ? '&recursive=true' : ''}`,
      { method: 'DELETE' },
    ),

  apps: () => request<App[]>('/api/apps'),
  app: (id: number) => request<App>(`/api/apps/${id}`),
  createApp: (input: AppInput) => request<App>('/api/apps', { method: 'POST', ...json(input) }),
  updateApp: (id: number, input: Partial<AppInput>) =>
    request<App>(`/api/apps/${id}`, { method: 'PATCH', ...json(input) }),
  deleteApp: (id: number) => request<void>(`/api/apps/${id}`, { method: 'DELETE' }),
  deploy: (appId: number, hostId: number, params: Record<string, string>) =>
    request<Deployment>(`/api/apps/${appId}/deploy`, { method: 'POST', ...json({ hostId, params }) }),

  installations: () => request<Installation[]>('/api/installations'),
  redeploy: (installationId: number, params?: Record<string, string>) =>
    request<Deployment>(`/api/installations/${installationId}/redeploy`, {
      method: 'POST',
      ...json({ params: params ?? {} }),
    }),
  checkInstallation: (id: number) =>
    request<{ healthStatus: string; healthDetail: string }>(`/api/installations/${id}/check`, {
      method: 'POST',
    }),
  /** Runs the app's uninstall command on the host and, once it succeeds,
   *  forgets the installation. It is a deployment like any other, so it comes
   *  back with an id to follow the log on. */
  uninstall: (id: number) =>
    request<Deployment>(`/api/installations/${id}/uninstall`, { method: 'POST' }),
  /** Drops HostMan's record and nothing else — whatever the app left on the
   *  host stays there. Uninstall is the one that removes it. */
  forgetInstallation: (id: number) =>
    request<void>(`/api/installations/${id}`, { method: 'DELETE' }),

  deployments: (params: { appId?: number; hostId?: number; limit?: number } = {}) => {
    const query = new URLSearchParams()
    if (params.appId) query.set('appId', String(params.appId))
    if (params.hostId) query.set('hostId', String(params.hostId))
    if (params.limit) query.set('limit', String(params.limit))
    const suffix = query.toString() ? `?${query}` : ''
    return request<Deployment[]>(`/api/deployments${suffix}`)
  },
  deployment: (id: number) => request<Deployment>(`/api/deployments/${id}`),
  cancelDeployment: (id: number) =>
    request<{ status: string }>(`/api/deployments/${id}/cancel`, { method: 'POST' }),

  self: () => request<SelfInfo>('/api/self'),
  selfUpdate: (ref: string) =>
    request<Deployment>('/api/self/update', { method: 'POST', ...json({ ref }) }),

  sshKey: () => request<SSHKeyInfo>('/api/settings/ssh'),
  rotateSSHKey: () => request<SSHKeyInfo>('/api/settings/ssh/rotate', { method: 'POST' }),
}
