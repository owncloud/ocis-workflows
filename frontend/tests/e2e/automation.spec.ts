import { test, expect } from '@playwright/test'
import { login } from './support/auth'

test('connect and disconnect background automation', async ({ page }) => {
  await login(page)
  await page.goto('/workflows/workflows')

  // Start from a known state regardless of what earlier runs left behind.
  const statusPill = page.locator('.workflows-automation-status .workflows-status-pill')
  if (await page.getByRole('button', { name: 'Disconnect automation' }).isVisible()) {
    await page.getByRole('button', { name: 'Disconnect automation' }).click()
    await expect(statusPill).toHaveText('Automation not connected')
  }

  await page.getByRole('button', { name: 'Connect automation' }).click()
  await expect(statusPill).toHaveText('Automation connected')
  await expect(page.getByRole('button', { name: 'Disconnect automation' })).toBeVisible()

  await page.reload()
  await expect(statusPill).toHaveText('Automation connected')

  await page.getByRole('button', { name: 'Disconnect automation' }).click()
  await expect(statusPill).toHaveText('Automation not connected')
  await expect(page.getByRole('button', { name: 'Connect automation' })).toBeVisible()
})
