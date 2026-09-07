export type HostStatus = 'unknown' | 'online' | 'offline' | 'error'

export interface Disk {
  mount: string
  device: string
  totalBytes: number
  usedBytes: number
}

export interface Sample {
  hostId: number
  takenAt: string
  cpuPct: number
  memUsed: number
  memTotal: number
  swapUsed: number
  swapTotal: number
  load1: number
  uptimeS: number
  tempC: number | null
  disks: Disk[]
}

/** What one metric did over a window: its floor, its ceiling and its mean. */
export interface Stat {
  min: number
  max: number
  avg: number
}

/** A host's CPU and memory over the last day, reduced by the server so the
 *  phone never carries a day of samples to work out six numbers. */
export interface MetricSummary {
  hostId: number
  since: string
  /** How many samples the numbers came from. Zero on a host with no history. */
  samples: number
  cpuPct: Stat
  /** The share of memory in use, and the same thing in bytes. */
  memPct: Stat
  memUsed: Stat
  memTotal: number
}

/** One process, as it was during the second the probe watched. */
export interface HostProcess {
  pid: number
  /** comm, which the kernel truncates to 15 characters. */
  name: string
  /** Its share of the whole machine over that second — the same scale as the
   *  host's own CPU figure, so four busy cores are 100% between them. */
  cpuPct: number
  /** Resident memory, and the same as a share of the host's total. */
  memBytes: number
  memPct: number
}

/** What a host was busy with at one moment. Not history: the server keeps only
 *  the newest snapshot, so this is null until the first probe after a restart. */
export interface HostProcesses {
  takenAt: string
  topCpu: HostProcess[]
  topMem: HostProcess[]
}

export interface Host {
  id: number
  name: string
  address: string
  port: number
  username: string
  status: HostStatus
  lastError: string
  lastSeenAt: string | null
  hostname: string
  os: string
  kernel: string
  arch: string
  sudoOk: boolean
  createdAt: string
  /** True for the machine HostMan itself runs on. */
  isSelf: boolean
  latest: Sample | null
}

export interface HostTestResult {
  ok: boolean
  error?: string
  sudoOk: boolean
  hostname?: string
  os?: string
  kernel?: string
  arch?: string
  hints?: string[]
}

export interface ProvisionStep {
  name: string
  ok: boolean
  detail?: string
}

/** The outcome of the one-time setup that authorizes HostMan on a host. */
export interface ProvisionResult {
  ok: boolean
  error?: string
  sudoOk: boolean
  steps: ProvisionStep[]
  hints?: string[]
}

/** What a directory entry is. A symlink keeps its own type and says what it
 *  resolves to, so the browser can open it as what it points at. */
export type EntryType = 'dir' | 'file' | 'link' | 'other'

export interface DirEntry {
  name: string
  type: EntryType
  /** For a symlink: 'dir', 'file', 'other', or 'broken' where it leads nowhere. */
  linkType?: EntryType | 'broken'
  /** The symlink as written, unresolved. */
  target?: string
  size: number
  /** Permission bits in octal, e.g. "644". */
  mode: string
  owner: string
  group: string
  modifiedAt: string
}

export interface DirListing {
  /** Where the host ended up, with symlinks resolved. */
  path: string
  parent: string
  entries: DirEntry[]
  /** True when the directory holds more than the listing returns. */
  truncated: boolean
  /** Who the commands ran as — root wherever passwordless sudo is set up. */
  asUser: string
}

/** What a directory holds, added up all the way down. */
export interface DirUsage {
  /** The directory measured, with a symlink resolved to where it led. */
  path: string
  /** Everything under it that is not a directory; a symlink counts as itself. */
  files: number
  /** Directories under it, not counting itself. */
  dirs: number
  /** Space on disk as du reports it: blocks allocated, not lengths added up. */
  bytes: number
  /** Places the walk could not enter. Above zero, the numbers are a floor. */
  unreadable: number
  asUser: string
}

export interface HostFile {
  path: string
  size: number
  mode: string
  owner: string
  group: string
  modifiedAt: string
  content: string
  /** Only the first part of the file came back. */
  truncated: boolean
  /** Not text: shown, never offered for editing. */
  binary: boolean
  asUser: string
}

export interface Crontab {
  user: string
  content: string
  /** False where the user has no crontab yet, which is not an error. */
  exists: boolean
}

/** One systemd service, as `systemctl show` describes it. HostMan lists the
 *  ones an administrator installed by hand, not the distribution's own. */
export interface ServiceUnit {
  name: string
  description: string
  /** systemd's LoadState: 'loaded', 'not-found', 'masked', 'error'. */
  load: string
  /** ActiveState: 'active', 'inactive', 'failed', 'activating', 'deactivating'. */
  active: string
  /** The finer state within active: 'running', 'exited', 'dead'. */
  sub: string
  /** UnitFileState: 'enabled', 'disabled', 'static', 'masked', or empty. */
  fileState: string
  /** The unit file systemd read, which is the one the editor opens. */
  path: string
  /** True for a foo@.service, which is a pattern rather than a service. */
  template: boolean
  /** True for a .timer, which runs nothing itself and starts another unit on a
   *  schedule. Most of the fields below mean something else on one. */
  timer: boolean
  /** For a timer: the unit it starts when it fires. */
  triggers?: string
  /** For a timer: seconds until it next fires. 0 where systemd does not say,
   *  which is what a stopped timer says. */
  nextS?: number
  /** For a timer: how long ago it last fired, in seconds. 0 where it never
   *  has. */
  lastS?: number
  mainPid: number
  /** What its cgroup is using now; 0 where nothing on the host counts it. */
  memory: number
  /** Where that figure came from: 'cgroup' is the kernel's own accounting for
   *  the unit, 'rss' is the resident size of its processes added up, which
   *  counts shared pages once per process. Absent where there is no figure,
   *  and on a listing, which does not ask. */
  memoryFrom?: 'cgroup' | 'rss'
  /** How many times systemd has restarted it by itself. */
  restarts: number
  /** How long it has been in its current state, in seconds. */
  sinceS: number
  /** Why it last stopped: 'success', 'exit-code', 'signal', 'timeout'. */
  result: string
  /** Why systemd could not read the unit file, where it could not. */
  loadError?: string
  /** The units that pull this one in, most specific relation first — the
   *  answer a 'static' unit file leaves out. Only the single-service call asks
   *  systemd for it, so it is absent on a listing. */
  startedBy?: string[]
}

export interface ServiceList {
  units: ServiceUnit[]
  /** Who the commands ran as — root wherever passwordless sudo is set up. */
  asUser: string
  /** The host has more hand-installed units than HostMan will list. */
  truncated: boolean
}

export interface ServiceLog {
  name: string
  lines: number
  content: string
  /** The log was longer than HostMan will carry and lost its beginning. */
  truncated: boolean
  asUser: string
}

/** Everything HostMan will ask systemd to do. */
export type ServiceAction = 'start' | 'stop' | 'restart' | 'reload' | 'enable' | 'disable'

/** An action answers with the service as it now is — or, where reading it back
 *  afterwards failed, with just what was done. */
export type ServiceActionResult = ServiceUnit | { name: string; action: string }

/** What HostMan thinks took a host down the last time it restarted. */
export type BootCause =
  | 'clean'
  | 'panic'
  | 'lockup'
  | 'oom'
  | 'overheat'
  | 'undervoltage'
  | 'power'
  | 'storage'
  | 'unknown'

/** How much the verdict is worth. 'certain' is only ever used where the machine
 *  said so in as many words; everything inferred is at best 'likely'. */
export type BootConfidence = 'certain' | 'likely' | 'unclear'

/** Where the evidence came from: systemd's journal, an rsyslog file where the
 *  journal keeps nothing across restarts, or nowhere at all. */
export type BootSource = 'journal' | 'logfile' | 'none'

/** One thing found in the record that bears on why the machine went down. */
export interface BootSign {
  kind: string
  label: string
  /** The log line it was found in, so the verdict can be checked. */
  line: string
  /** The part worth pulling out: the panic's reason, the process the
   *  out-of-memory killer picked. */
  detail?: string
  /** How many seconds before the machine came back this was logged. */
  beforeS?: number
  /** Close enough to the restart to be part of it, rather than something that
   *  also happened during that boot. */
  near: boolean
  count: number
}

/** One time the machine came up, as wtmp remembers it. */
export interface Restart {
  bootedAt: string
  /** How long that boot lasted, or how long the current one has been running. */
  upS?: number
  /** True where a shutdown was recorded before it — something asked. Only
   *  meaningful when the report's cleanKnown is set. */
  clean: boolean
  kernel?: string
  current?: boolean
  /** False where the record was found but its time was not, which is what an
   *  old `last` with no ISO stamps leaves behind. */
  timed: boolean
}

/** The Raspberry Pi firmware's own account of its power and heat. The "since
 *  boot" flags describe the boot that is running now, not the one that ended,
 *  which makes them corroboration rather than proof. */
export interface Throttle {
  raw: string
  underVoltageNow: boolean
  cappedNow: boolean
  throttledNow: boolean
  softTempNow: boolean
  underVoltage: boolean
  capped: boolean
  throttled: boolean
  softTemp: boolean
}

/** What the hardware watchdog says about the reset that started this boot. Not
 *  every driver fills it in, so it is shown and never relied on. */
export interface Watchdog {
  bootStatus: number
  flags?: string[]
}

/** HostMan's answer to "why did it restart?", with everything it looked at. */
export interface BootReport {
  cause: BootCause
  confidence: BootConfidence
  headline: string
  detail: string
  /** The steps to the verdict, in the order they mattered. */
  reasons: string[]

  bootedAt: string
  uptimeS: number
  /** How long the boot before this one lasted. */
  previousUpS?: number

  model?: string
  kernel?: string
  /** What it was running before, where that is known and different — a restart
   *  that changed the kernel was an update. */
  previousKernel?: string

  signs: BootSign[]
  restarts: Restart[]
  /** How many of those restarts nothing asked for. */
  unclean: number
  /** Whether the history can tell a restart that was asked for from one that
   *  was not. busybox's `last` cannot. */
  cleanKnown: boolean

  source: BootSource
  /** Whether the host has journalctl at all. */
  journal: boolean
  /** Whether its journal survives a restart. False is the reason this screen
   *  usually has nothing to show. */
  persistent: boolean
  /** The rsyslog file the evidence came out of, where the journal had none. */
  logFile?: string
  bootsKept?: number

  /** The last thing the machine said before it went. */
  logTail: string
  truncated: boolean

  throttle?: Throttle
  watchdog?: Watchdog
  tempC?: number
  asUser: string
}

/** The state of persistent logging on a host, and what came of turning it on. */
export interface JournalStorage {
  enabled: boolean
  /** True where it was already on and HostMan changed nothing. */
  already: boolean
  /** What journald.conf says about Storage=, where it says anything. */
  configured?: string
  /** Why the journal still will not survive a restart, where it will not. */
  blocked?: string
}

/** Where a host's remote session got to. "absent" means it has never been set
 *  up here; "running" is an install in flight; "failed" carries the exit status
 *  of the one that did not finish. */
export type RemoteSetupState = 'absent' | 'running' | 'ok' | 'failed'

/** One file in the host's downloads directory — the point of the whole screen,
 *  so the newest few are reported with it. */
export interface RemoteFile {
  name: string
  size: number
  /** How long ago it was written, as the host counts. */
  ageS: number
}

/** A browser running on the host, driven from the phone: everything the screen
 *  needs about it in one answer. */
export interface RemoteSession {
  /** The systemd unit, so the session is a service like any other. */
  unit: string
  setup: RemoteSetupState
  /** The exit status behind a failed setup. */
  setupExit?: number
  /** The tail of the install log: what it is doing, or why it stopped. */
  setupLog?: string
  /** Whether a session could be started right now. */
  ready: boolean
  /** What is not installed yet, where that is what stands in the way. */
  missing?: string[]
  /** The browser the session will run, as the host names it. */
  browser?: string
  running: boolean
  /** systemd's own words, for when "running" is not the whole story. */
  active?: string
  sub?: string
  port: number
  geometry: string
  /** Generated on the host and kept there; shown so it can be pasted, and
   *  carried in the link so nobody has to type it on a phone. */
  password?: string
  /** The page the session opens with. */
  homepage?: string
  /** The browsers this host has only as snaps, which are no use in a session:
   *  confinement walls them out of the profile directory. Naming them is what
   *  tells "no browser" apart from "one that looks installed and cannot run". */
  snapBrowser?: string
  /** Browsers that are installed here and will not run — they cannot report
   *  even their own version. Same fix as a snap, different sentence. */
  brokenBrowser?: string
  /** True where the browser here runs without its own sandbox, because this
   *  host would not give it one. A weaker browser than the phone's, and worth
   *  saying so on the screen rather than only in the journal. */
  noSandbox?: boolean
  /** True where the host is running scripts an older HostMan wrote. Updating
   *  HostMan does not rewrite what is on a host — setting up again does. */
  stale?: boolean
  /** The account it runs as — whose Downloads a file lands in. */
  user: string
  downloads?: string
  profile?: string
  files: RemoteFile[]
  /** Where noVNC answers on this host, ready to connect. */
  url: string
}

/**
 * The downloader: deluge running on the host, driven from here.
 *
 * One object answers the four questions the screen asks in order — is deluge
 * installed, has HostMan set it up, is it running, and what is it doing —
 * because each of them is only worth asking if the one before it was yes.
 */
export interface TorrentDaemon {
  /** The systemd unit, so the daemon is a service like any other. */
  unit: string
  /** Whether deluge is on the host at all. It is the one thing on this screen
   *  HostMan will not install for you. */
  installed: boolean
  /** The deluge commands that are missing, which the screen turns into the
   *  line to run. */
  missing?: string[]
  /** deluged's own version. */
  version?: string
  /** Whether HostMan has written the daemon onto this host. */
  configured: boolean
  /** Whether a torrent could be added right now. */
  ready: boolean
  running: boolean
  /** Whether it comes back after a reboot. Unlike the remote session, it
   *  does — a download that takes six hours should survive a restart. */
  enabled: boolean
  /** systemd's own words, for when "running" is not the whole story. */
  active?: string
  sub?: string
  /** True where the host is running a unit an older HostMan wrote. */
  stale?: boolean
  /** The account it runs as, and so the account that owns the files. */
  user: string
  /** Where the files land. Before setup it is the folder HostMan would use. */
  downloads: string
  /** The disk behind that folder, in bytes. A torrent that fills a Pi's card
   *  is the ordinary way this goes wrong. */
  free?: number
  capacity?: number
  torrents: Torrent[]
  /** Whether deluge itself answered this time. When it is false the list is
   *  empty because nobody could be asked — the daemon is stopped, or it did
   *  not answer — rather than because there is nothing to download. */
  asked: boolean
  /** What deluge does with a torrent once it has finished downloading. */
  seeding: TorrentSeeding
  /** How many torrents deluge works on at once — the rest wait as Queued
   *  until a slot opens. -1 is no limit at all; absent where deluge could not
   *  be asked. */
  activeLimit?: number
  /** What deluge said when it could not be asked. A state to report rather
   *  than an error: everything else on the screen is still true. */
  trouble?: string
}

/** What becomes of a torrent once it has finished. Left alone deluge seeds for
 *  ever, which is polite and is also how a list nobody is watching fills up
 *  with things that finished last month. */
export interface TorrentSeeding {
  /** What a torrent has to upload, against what it downloaded, before deluge
   *  stops seeding it. Zero means never stop. */
  ratio: number
  /** Whether the entry then goes from the list. The files are never touched. */
  remove: boolean
}

export interface Torrent {
  /** The info hash deluge knows it by, and what an action names. */
  id: string
  /** Until a magnet link's metadata arrives, deluge answers with the hash. */
  name: string
  /** Deluge's own word: Downloading, Seeding, Paused, Queued, Checking. */
  state: string
  /** 0 to 100. */
  progress: number
  /** Bytes: what has arrived, and what the whole thing is. */
  done: number
  size: number
  /** Bytes per second. */
  down: number
  up: number
  /** Seconds left, and deluge's own words for the same thing — "∞" says
   *  something a zero cannot. */
  eta?: number
  etaText?: string
  ratio?: number
  /** Where this torrent's own files are going — the folder the downloader had
   *  when it was added, which is not always the one it has now. */
  folder?: string
  /** Connected, and visible: the two numbers deluge prints as "4 (23)". */
  seeds: number
  seedsTotal: number
  peers: number
  peersTotal: number
}

export type HealthType = 'none' | 'http' | 'systemd'
export type HealthStatus = 'unknown' | 'passing' | 'failing' | 'unchecked'

export interface Param {
  name: string
  label: string
  default: string
  help: string
  required: boolean
}

export interface App {
  id: number
  name: string
  description: string
  installCommand: string
  /** How the app takes itself back off a host. Empty for an app that never
   *  said, which can only be forgotten, never uninstalled. */
  uninstallCommand: string
  params: Param[]
  healthType: HealthType
  healthTarget: string
  createdAt: string
  /** True for the app that installs HostMan itself. */
  selfUpdate: boolean
}

export type DeploymentStatus = 'running' | 'succeeded' | 'failed' | 'canceled' | 'interrupted'

/** Which of an app's two commands a run was. */
export type DeploymentKind = 'install' | 'uninstall'

export interface Deployment {
  id: number
  appId: number
  hostId: number
  command: string
  params: Record<string, string>
  kind: DeploymentKind
  status: DeploymentStatus
  exitCode: number | null
  error: string
  log?: string
  startedAt: string
  finishedAt: string | null
  appName?: string
  hostName?: string
}

export interface Installation {
  id: number
  appId: number
  hostId: number
  params: Record<string, string>
  lastDeploymentId: number | null
  healthStatus: HealthStatus
  healthDetail: string
  healthCheckedAt: string | null
  installedAt: string
  updatedAt: string
  appName: string
  hostName: string
  hostAddress: string
  healthType: HealthType
  healthTarget: string
  lastStatus: DeploymentStatus | ''
  /** Ports the app answers on, worked out from its health check and its
   *  parameters. Absent when neither of them says. */
  ports?: number[]
  /** The version deployed here, worked out from the parameters that named it.
   *  Absent for an app whose command installs whatever is current. */
  version?: string
  /** Where to open the app in a browser, from the same two sources. Absent
   *  when neither of them says enough to name an address. */
  url?: string
}

/** A login shell open on a host. It belongs to HostMan rather than to the
 *  screen looking at it, so the same shell survives the phone locking, and two
 *  screens can be looking at one. */
export interface ShellSession {
  id: string
  hostId: number
  /** Who the shell runs as: the SSH user, never root by way of sudo. */
  user: string
  cols: number
  rows: number
  startedAt: string
  /** How many bytes the shell has produced. A screen that is reconnecting asks
   *  the stream to start where it stopped rather than from the beginning. */
  offset: number
  running: boolean
  /** How it ended, in words. Empty while it is running. */
  exit: string
  /** How many screens are attached — how you find out a second phone is
   *  looking at the same shell. */
  watchers: number
}

export interface Overview {
  hosts: Host[]
  installations: Installation[]
}

export interface SelfInfo {
  version: string
  machineId: string
  host: Host | null
  app: App | null
  ref: string
  runningDeploymentId: number | null
  ready: boolean
  blocked?: string
}

export interface SSHKeyInfo {
  publicKey: string
  fingerprint: string
  authorizeCommand: string
  sudoCommand: string
}

/** Claude Code on a host, for the SSH user: whether it is there, whether that
 *  user is signed in, and how far an install or a sign-in has got. */
export interface ClaudeHost {
  user: string
  home: string
  installed: boolean
  version?: string
  path?: string
  arch?: string
  os?: string
  /** The install HostMan started, if it started one. */
  install: 'absent' | 'running' | 'failed' | 'done'
  installExit?: number
  installLog?: string
  signedIn: boolean
  /** How: a Claude account, or an API key. */
  auth?: 'oauth' | 'api_key' | ''
  account?: string
  plan?: string
  /** The sign-in HostMan started. "waiting" means the link is ready and a
   *  code is what it needs. */
  login: 'absent' | 'waiting' | 'running' | 'failed' | 'done'
  loginUrl?: string
  loginExit?: number
  loginLog?: string
  /** Installed and signed in: a session could start now. */
  ready: boolean
}

export type ClaudeMode = 'default' | 'acceptEdits' | 'plan' | 'bypassPermissions'

/** One conversation with Claude Code on a host. It lives on HostMan, so it
 *  outlives the screen looking at it. */
export interface ClaudeSession {
  id: string
  hostId: number
  user: string
  name: string
  dir: string
  /** As the CLI reports them; an alias asked for at the start comes back as
   *  the model it resolved to. */
  model: string
  mode: ClaudeMode | string
  cliSessionId: string
  startedAt: string
  /** How many events the session has produced; a reconnecting screen asks the
   *  stream to start here. */
  offset: number
  running: boolean
  /** Claude is working on a turn. */
  busy: boolean
  /** Permission requests waiting for an answer. */
  pending: number
  exit: string
  watchers: number
  cost: number
  turns: number
  context: number
  contextWindow: number
}

/** One piece of something Claude said. */
export interface ClaudeBlock {
  type: 'text' | 'thinking' | 'tool_use'
  text?: string
  id?: string
  name?: string
  input?: unknown
}

/** One event in a session's history. Which fields are set depends on kind. */
export interface ClaudeEntry {
  seq: number
  at: string
  kind:
    | 'init'
    | 'assistant'
    | 'tool_result'
    | 'permission'
    | 'permission_cancelled'
    | 'result'
    | 'notice'
    | 'user'
    | 'answered'
    | 'model'
    | 'mode'
    | 'exit'
  model?: string
  mode?: string
  cwd?: string
  blocks?: ClaudeBlock[]
  context?: number
  toolUseId?: string
  content?: string
  isError?: boolean
  requestId?: string
  toolName?: string
  input?: unknown
  description?: string
  behavior?: 'allow' | 'deny'
  cost?: number
  turns?: number
  durationMs?: number
  contextWindow?: number
  text?: string
  subtype?: string
}
