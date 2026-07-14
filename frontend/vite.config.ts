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
  }
})
