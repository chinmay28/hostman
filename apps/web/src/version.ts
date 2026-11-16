/**
 * The running client's version, `vYEAR.MONTH.PATCH`, where the leading numbers
 * are the year and month of the commit it was built from and the patch number
 * is the repository's commit count (so `v2026.11.42` is the 42nd commit, made
 * in November 2026).
 *
 * Inlined at build time by Vite's `define` (see vite.config.ts) from
 * scripts/version.mjs — the same source the binary is stamped from, so the
 * header and Settings always agree. Patch `0` means a build made without git
 * available.
 */
declare const __APP_VERSION__: string

export const APP_VERSION: string = __APP_VERSION__
