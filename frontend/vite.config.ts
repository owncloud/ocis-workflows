import { defineConfig } from '@ownclouders/extension-sdk'

export default defineConfig({
  name: 'web-app-workflows',
  server: {
    port: 9225
  },
  build: {
    rollupOptions: {
      output: {
        entryFileNames: 'workflows.js'
      }
    }
  },
  test: {
    // extension-sdk's own default only excludes a top-level e2e/ dir; ours lives at
    // tests/e2e, and Playwright specs there must not be picked up by vitest.
    exclude: ['**/node_modules/**', '**/dist/**', 'tests/e2e/**']
  }
})
