import { defineConfig } from 'vitest/config'

// Kept apart from vite.config.ts: that one stamps the version and proxies the
// API, neither of which a unit test wants.
export default defineConfig({
  // The header shows the version, which the build inlines; a test has no
  // build, so it gets a version that is visibly not one.
  define: { __APP_VERSION__: JSON.stringify('v0.0.0') },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.{ts,tsx}'],
  },
})
