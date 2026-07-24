import { test, expect } from '@playwright/test'
import { login } from './support/auth'

test('build a workflow with a file event trigger and persist its filters', async ({ page }) => {
  await login(page)

  await page.goto('/workflows/workflows')
  await expect(page.getByRole('heading', { name: 'Workflows' })).toBeVisible()

  await page.getByRole('button', { name: 'Add workflow' }).click()
  await page.waitForURL(/\/workflows\/workflows\/new$/)

  await page.getByRole('button', { name: 'Add trigger' }).click()
  await page.getByRole('button', { name: 'File Event Trigger', exact: true }).click()
  await expect(page.locator('.workflows-node-trigger')).toBeVisible()

  // Adding the node opens its Node Details panel automatically; configure the event
  // type + path filter + space directly, no extra click needed to open it.
  await page.getByLabel('Event').selectOption('move')
  await page.getByLabel('Only for files under path (optional)').fill('/Invoices')

  // Exactly 2 options in this dev stack: "Any space" plus the admin account's one real
  // (non-virtual) space — assert the count rather than hardcoding the space's display name.
  const spaceSelect = page.getByLabel('Space (optional)')
  await expect(spaceSelect.locator('option')).toHaveCount(2)
  const spaceValue = await spaceSelect.locator('option').nth(1).getAttribute('value')
  await spaceSelect.selectOption({ index: 1 })

  await page.getByRole('button', { name: 'Close' }).click()

  await page.locator('.workflows-node-trigger .workflows-node-add-button').click()
  await page.getByRole('button', { name: 'LLM Prompt', exact: true }).click()
  await expect(page.locator('.workflows-node-llm')).toBeVisible()

  // Close the LLM node's auto-opened config panel before continuing.
  await page.getByRole('button', { name: 'Close' }).click()

  const workflowName = `e2e event trigger workflow ${Date.now()}`
  await page.getByRole('button', { name: 'Untitled workflow' }).click()
  await page.getByLabel('Workflow name').fill(workflowName)
  await page.getByLabel('Workflow name').press('Enter')

  await page.getByRole('button', { name: 'Save' }).click()
  await page.waitForURL(/\/workflows\/workflows\/(?!new$)[\w-]+$/)
  const workflowUrl = page.url()

  // Reload from scratch (hard navigation, matching this app's known-reliable navigation
  // pattern — see src/router.ts) and confirm the event trigger's type and filter were
  // actually persisted, not just held in local component state.
  await page.goto(workflowUrl)
  await page.locator('.workflows-node-trigger').click()
  await expect(page.getByLabel('Trigger type')).toHaveValue('event')
  await expect(page.getByLabel('Event')).toHaveValue('move')
  await expect(page.getByLabel('Only for files under path (optional)')).toHaveValue('/Invoices')
  await expect(page.getByLabel('Space (optional)')).toHaveValue(spaceValue!)
  await page.getByRole('button', { name: 'Close' }).click()

  await page.goto('/workflows/workflows')
  const row = page.getByRole('row').filter({ hasText: workflowName })
  await expect(row).toBeVisible()
  await expect(row.getByText('Active')).toBeVisible()

  // Clean up via the UI's own delete flow — exercises it and leaves no test data behind.
  await row.getByRole('button', { name: 'Delete' }).click()
  await expect(row).toBeHidden()
})
