import type { Page } from '@playwright/test'

/** Logs in through oCIS's real sign-in page and waits for the files view to load. */
export async function login(page: Page, username = 'admin', password = 'admin'): Promise<void> {
  await page.goto('/')
  await page.getByLabel(/username/i).fill(username)
  await page.getByLabel(/password/i).fill(password)
  await page.getByRole('button', { name: /log ?in|sign ?in/i }).click()
  await page.waitForURL(/\/files\//, { timeout: 30000 })
}
