#!/usr/bin/env node
/**
 * The one place HostMan's version number is assembled.
 *
 * Scheme: vYEAR.MONTH.PATCH — a calendar version, where PATCH is the
 * repository's commit count, so `v2026.8.42` is the 42nd commit on the 2026.8
 * line. The month is not zero-padded; that keeps the string valid semver.
 *
 *   - YEAR/MONTH are source constants, read out of
 *     server/internal/version/version.go so there is exactly one declaration
 *     of them in the tree. `--bump` moves them to the month a branch opens in
 *     (see CLAUDE.md); they are not taken from the build clock, which would
 *     move the version without a commit.
 *   - PATCH comes from `git rev-list --count HEAD`, which only exists at build
 *     time: the Go binary gets it stamped in by -ldflags, the web bundle gets
 *     it inlined by Vite. Both call this file, so they can never disagree.
 *
 * Usage:
 *   node scripts/version.mjs            # print e.g. v2026.8.42
 *   node scripts/version.mjs --patch    # print just the commit count (42)
 *   node scripts/version.mjs --bump     # set YEAR.MONTH to this month, UTC
 *   node scripts/version.mjs --bump 2026.9   # or to a month named outright
 *   import { appVersion } from './scripts/version.mjs'
 */
import { execFileSync } from 'node:child_process'
import { readFileSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const GO_VERSION_FILE = resolve(repoRoot, 'server/internal/version/version.go')

/** Read `Year`/`Month` out of the Go source that declares them. */
function yearMonth() {
  const src = readFileSync(GO_VERSION_FILE, 'utf8')
  const read = (name) => {
    const m = new RegExp(`^\\s*${name}\\s*=\\s*(\\d+)\\s*$`, 'm').exec(src)
    if (!m) {
      throw new Error(`could not find ${name} in ${GO_VERSION_FILE}`)
    }
    return Number(m[1])
  }
  const year = read('Year')
  const month = read('Month')
  if (!(month >= 1 && month <= 12)) {
    throw new Error(`Month = ${month} in ${GO_VERSION_FILE}; want a calendar month (1-12)`)
  }
  return { year, month }
}

/**
 * Rewrite Year/Month in the Go source to `line` — "YYYY.M", or this month in
 * UTC when not given. Returns what the line was and what it is now, so the
 * caller can say whether anything moved. The same regex that reads the
 * constants writes them, so the file keeps the shape the Go test checks.
 */
export function bumpYearMonth(line) {
  const target = line ? parseLine(line) : thisMonth()
  const before = yearMonth()
  let src = readFileSync(GO_VERSION_FILE, 'utf8')
  for (const [name, value] of [['Year', target.year], ['Month', target.month]]) {
    src = src.replace(new RegExp(`^(\\s*${name}\\s*=\\s*)\\d+(\\s*)$`, 'm'), `$1${value}$2`)
  }
  writeFileSync(GO_VERSION_FILE, src)
  return { before, after: yearMonth() }
}

function thisMonth() {
  const now = new Date()
  return { year: now.getUTCFullYear(), month: now.getUTCMonth() + 1 }
}

function parseLine(line) {
  const m = /^(\d{4})\.(\d{1,2})$/.exec(line)
  if (!m || Number(m[2]) < 1 || Number(m[2]) > 12) {
    throw new Error(`--bump wants a line like 2026.9, got ${JSON.stringify(line)}`)
  }
  return { year: Number(m[1]), month: Number(m[2]) }
}

/** Run git in the repo root; null if it fails (no repo, no git, old git). */
function git(args) {
  try {
    return execFileSync('git', args, {
      cwd: repoRoot,
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'ignore'],
    }).trim()
  } catch {
    return null
  }
}

/**
 * The commit count on HEAD, or '0' when it can't be known — no repo (a tarball,
 * or a copy that skipped `.git`), no git, or a **shallow** clone.
 *
 * Shallow is the trap, and it's why this isn't a bare `rev-list`: a clone made
 * with `--depth 1` answers `rev-list --count HEAD` with `1`, which is not an
 * error and not obviously wrong — it would just quietly ship a build calling
 * itself `2026.8.1`. Refuse it. Patch 0 is the agreed "unstamped build" marker (it
 * matches the Go default), and a version ending in `.0` is visibly a
 * non-release rather than a plausible lie.
 *
 * Anything building a release therefore needs the full commit graph:
 * `--filter=blob:none` rather than `--depth 1` for a cheap clone that still
 * carries all of it, which is what scripts/quickstart.sh does.
 */
export function commitCount() {
  if (git(['rev-parse', '--is-shallow-repository']) === 'true') {
    process.emitWarning(
      'shallow git clone — the commit count is not the real one, reporting patch 0. ' +
        'Clone with --filter=blob:none (or fetch --unshallow) for a real version.',
    )
    return '0'
  }
  // A failed probe (git older than 2.15, or no repo at all) is not proof of
  // shallowness — fall through and let the count itself answer.
  return git(['rev-list', '--count', 'HEAD']) ?? '0'
}

/**
 * The full version string, `v`-prefixed to match how the project tags releases.
 * Must stay byte-identical to version.String() in the Go package.
 *
 * Note `--patch` / commitCount() stays bare: that one feeds `-ldflags -X` as
 * the value of `version.Patch`, which is the number alone.
 */
export function appVersion() {
  const { year, month } = yearMonth()
  return `v${year}.${month}.${commitCount()}`
}

// Invoked directly (by the build scripts), print rather than export.
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2)
  const bump = args.indexOf('--bump')
  if (bump >= 0) {
    const { before, after } = bumpYearMonth(args[bump + 1])
    const moved = before.year !== after.year || before.month !== after.month
    process.stdout.write(
      moved
        ? `version line ${before.year}.${before.month} -> ${after.year}.${after.month}\n`
        : `version line already ${after.year}.${after.month}\n`,
    )
  } else {
    process.stdout.write(args.includes('--patch') ? commitCount() : appVersion())
    process.stdout.write('\n')
  }
}
