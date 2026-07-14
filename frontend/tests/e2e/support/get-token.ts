// Logs in to a running oCIS instance via a real headless-browser session and prints the
// resulting OIDC access token as JSON to stdout.
//
// This exists because oCIS's built-in IdP sign-in page hashes credentials client-side
// before submitting them — there's no plain HTTP request an e2e suite can replay to get a
// token, short of reimplementing that JS. A real (headless) browser session is the only
// practical way to acquire one, so this script is the single shared credential-acquisition
// path for both the frontend Playwright suite and the backend Go e2e suite (which shells
// out to this script once per run, then does all its actual assertions over plain HTTP).
//
// Usage: node get-token.ts <baseUrl> <username> <password>

import { chromium } from '@playwright/test'

interface StoredOAuthUser {
  access_token: string
  expires_at: number
}

const [, , baseUrl, username, password] = process.argv
if (!baseUrl || !username || !password) {
  console.error('usage: get-token.ts <baseUrl> <username> <password>')
  process.exit(1)
}

const browser = await chromium.launch()
const context = await browser.newContext({ ignoreHTTPSErrors: true })
const page = await context.newPage()

try {
  await page.goto(baseUrl)
  await page.getByLabel(/username/i).fill(username)
  await page.getByLabel(/password/i).fill(password)
  await page.getByRole('button', { name: /log ?in|sign ?in/i }).click()
  await page.waitForURL(/\/files\//, { timeout: 30000 })

  const user = await page.evaluate<StoredOAuthUser | null>(() => {
    const key = Object.keys(localStorage).find((k) => k.startsWith('oc_oAuth.user:'))
    if (!key) return null
    return JSON.parse(localStorage.getItem(key) as string)
  })

  if (!user) {
    throw new Error('no oc_oAuth.user:* entry found in localStorage after login')
  }

  process.stdout.write(JSON.stringify({ accessToken: user.access_token, expiresAt: user.expires_at }))
} finally {
  await browser.close()
}
