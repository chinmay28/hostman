#!/usr/bin/env node
/**
 * The one place HostMan's version number is assembled.
 *
 * Scheme: vYEAR.MONTH.PATCH — a calendar version, where PATCH is the
 * repository's commit count, so `v2026.11.42` is the 42nd commit, made in
 * November 2026. The month is not zero-padded; that keeps the string valid
 * semver.
 *
 *   - YEAR/MONTH are the month of the commit being built — its committer date,
 *     in the committer's own timezone. Nobody bumps them: the line moves the
 *     moment a commit lands in a new month, and a rebuild of an old commit is
 *     still named what it shipped as, because a commit's date does not change.
 *     Not the build clock, which would move the version without a commit.
 *   - PATCH comes from `git rev-list --count HEAD`, which only exists at build
 *     time: the Go binary gets both stamped in by -ldflags (version.Patch and
 *     version.Date), the web bundle gets the string inlined by Vite, and the
 *     Android app gets it as its versionName/versionCode. All of them call
 *     this file, so they can never disagree.
 *
 * Usage:
 *   node scripts/version.mjs            # print e.g. v2026.11.42
 *   node scripts/version.mjs --patch    # print just the commit count (42)
 *   node scripts/version.mjs --date     # print the commit's month (2026-11), for version.Date
 *   node scripts/version.mjs --ldflags  # print the -X flags that stamp both into a Go build
 *   import { appVersion } from './scripts/version.mjs'
 */
import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')

/** The Go package the link-time values live in. */
export const VERSION_PKG = 'github.com/chinmay28/hostman/server/internal/version'

/**
 * The month of HEAD's commit as "YYYY-MM" — the value version.Date is stamped
 * with — or, with no commit to ask (a tarball, no git), the month of the build
 * clock in UTC, which is what the Go package falls back to as well, so an
 * unstamped build and a stamped one of the same tree agree.
 *
 * The committer date rather than the author's: a change authored in October
 * and landed in November is a November release. In the committer's own
 * timezone rather than UTC, so the month is the one on the commit as git shows
 * it.
 */
export function commitMonth() {
  const iso = git(['log', '-1', '--format=%cI'])
  const m = iso && /^(\d{4})-(\d{2})/.exec(iso)
  if (m) {
    return `${m[1]}-${m[2]}`
  }
  const t = new Date()
  return `${t.getUTCFullYear()}-${String(t.getUTCMonth() + 1).padStart(2, '0')}`
}

/** Year and month as numbers, the way the version string wants them. */
function yearMonth() {
  const [year, month] = commitMonth().split('-').map(Number)
  if (!(month >= 1 && month <= 12)) {
    throw new Error(`commit month ${commitMonth()} is not a calendar month`)
  }
  return { year, month }
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
 * carries all of it, which is what scripts/quickstart.sh does, and `fetch-depth: 0` on GitHub
 * Actions, which is what the release workflow does.
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
 * Note `--patch` / commitCount() and `--date` / commitMonth() stay bare: those
 * feed `-ldflags -X` as the values of `version.Patch` and `version.Date`.
 */
export function appVersion() {
  const { year, month } = yearMonth()
  return `v${year}.${month}.${commitCount()}`
}

/** The -X flags that stamp this tree's version into a Go build. */
export function ldflags() {
  return `-X ${VERSION_PKG}.Patch=${commitCount()} -X ${VERSION_PKG}.Date=${commitMonth()}`
}

// Invoked directly (by the build scripts), print rather than export.
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2)
  process.stdout.write(
    args.includes('--patch')
      ? commitCount()
      : args.includes('--date')
        ? commitMonth()
        : args.includes('--ldflags')
          ? ldflags()
          : appVersion(),
  )
  process.stdout.write('\n')
}
