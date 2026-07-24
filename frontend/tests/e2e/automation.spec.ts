import { test, expect } from '@playwright/test'
import { login } from './support/auth'

test('background execution connects automatically and can be disconnected', async ({ page }) => {
  // Automation connect/disconnect state is per-account, and every other e2e spec logs in as
  // the default `admin` user and runs concurrently with this one (`fullyParallel` in
  // playwright.config.ts). event-trigger.spec.ts in particular also silently connects
  // automation (its event-triggered workflow needs it too), which races with this test's own
  // connect/disconnect assertions if both run against the same account at once. Logging in as
  // a distinct demo user isolates this test's automation state from the rest of the suite.
  await login(page, 'einstein', 'relativity')
  await page.goto('/workflows/workflows')
  await expect(page.getByRole('heading', { name: 'Workflows' })).toBeVisible()
  // The workflow list (and automation status) loads asynchronously after mount — wait for
  // that to settle before inspecting rows below, otherwise `.all()` (which doesn't retry)
  // can run before the table exists and silently find nothing.
  await expect(page.getByText('Loading workflows...')).toBeHidden()

  // Start from a known state regardless of what earlier runs left behind: delete any
  // leftover workflows from this test, then disconnect automation if it's still connected
  // (safe to do unconditionally once those workflows are gone — nothing left to warn about).
  for (const row of await page.getByRole('row').filter({ hasText: 'e2e automation workflow' }).all()) {
    await row.getByRole('button', { name: 'Delete' }).click()
    await expect(row).toBeHidden()
  }
  if (await page.getByRole('button', { name: 'manage' }).isVisible()) {
    await page.getByRole('button', { name: 'manage' }).click()
    await page.getByRole('button', { name: 'Disconnect' }).click()
    await expect(page.getByText('Background execution active')).toBeHidden()
  }

  // Build a workflow with a manual trigger first and save it — this exercises the "existing
  // workflow" update path (no hard navigation), which is where we can reliably observe the
  // one-time connect toast. Creating a workflow with a schedule trigger from scratch instead
  // hard-navigates to the new workflow's URL immediately after save, before a toast could be
  // observed.
  await page.getByRole('button', { name: 'Add workflow' }).click()
  await page.waitForURL(/\/workflows\/workflows\/new$/)

  await page.getByRole('button', { name: 'Add trigger' }).click()
  await page.getByRole('button', { name: 'Manual Trigger', exact: true }).click()
  await expect(page.locator('.workflows-node-trigger')).toBeVisible()

  const workflowName = `e2e automation workflow ${Date.now()}`
  await page.getByRole('button', { name: 'Untitled workflow' }).click()
  await page.getByLabel('Workflow name').fill(workflowName)
  await page.getByLabel('Workflow name').press('Enter')

  await page.getByRole('button', { name: 'Save' }).click()
  await page.waitForURL(/\/workflows\/workflows\/(?!new$)[\w-]+$/)

  // Still a manual trigger — no automation involved yet.
  await expect(page.getByText('Background execution enabled for your account', { exact: true })).toBeHidden()

  // Switch to a schedule trigger and save again — the "existing workflow" path, where
  // silent connect + the one-time toast fire with no button click involved.
  await page.locator('.workflows-node-trigger').click()
  await page.getByLabel('Trigger type').selectOption('schedule')
  await page.getByRole('button', { name: 'Close' }).click()
  await page.getByRole('button', { name: 'Save' }).click()

  await expect(page.getByText('Background execution enabled for your account', { exact: true })).toBeVisible()

  await page.goto('/workflows/workflows')
  await expect(page.getByText('Background execution active')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Connect automation' })).toHaveCount(0)

  // Disconnecting while the workflow is still active shows the warning.
  await page.getByRole('button', { name: 'manage' }).click()
  await page.getByRole('button', { name: 'Disconnect' }).click()
  await expect(page.getByText('will stop running in the background', { exact: false })).toBeVisible()
  await page.getByRole('button', { name: 'Yes, disconnect' }).click()
  await expect(page.getByText('Background execution active')).toBeHidden()
  await expect(page.getByText('Background execution off')).toBeVisible()

  // Clean up via the UI's own delete flow.
  const row = page.getByRole('row').filter({ hasText: workflowName })
  await row.getByRole('button', { name: 'Delete' }).click()
  await expect(row).toBeHidden()
})
