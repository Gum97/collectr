import { defineConfig } from 'vitest/config'

// Kept out of vite.config.ts: the build config and the test config pull in
// different type versions of the plugin array, and merging them makes the type
// checker complain about a build that is otherwise correct.
export default defineConfig({
  test: { environment: 'node', include: ['src/**/*.test.ts'] },
})
