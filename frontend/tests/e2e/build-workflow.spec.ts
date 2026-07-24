import { test, expect } from '@playwright/test'
import { login } from './support/auth'

test('build and save a trigger -> LLM -> action workflow using the node picker', async ({ page }) => {
  await login(page)

  await page.goto('/workflows/workflows')
  await expect(page.getByRole('heading', { name: 'Workflows' })).toBeVisible()

  await page.getByRole('button', { name: 'Add workflow' }).click()
  await page.waitForURL(/\/workflows\/workflows\/new$/)

  // Empty canvas prompts for a trigger first, n8n-style.
  await expect(page.getByText('Add a trigger to start this workflow')).toBeVisible()
  await page.getByRole('button', { name: 'Add trigger' }).click()
  await page.getByRole('button', { name: 'Manual Trigger', exact: true }).click()
  await expect(page.locator('.workflows-node-trigger')).toBeVisible()

  // Adding the node opens its Node Details panel automatically; nothing to configure
  // for a manual trigger, so just close it before continuing.
  await page.getByRole('button', { name: 'Close' }).click()

  // Chain an LLM step off the trigger's "+" handle.
  await page.locator('.workflows-node-trigger .workflows-node-add-button').click()
  await page.getByRole('button', { name: 'LLM Prompt', exact: true }).click()
  await expect(page.locator('.workflows-node-llm')).toBeVisible()

  // The LLM node's config panel is already open; configure it directly.
  await page.getByLabel('Prompt', { exact: true }).fill('Summarize this file in three bullet points.')
  await page.getByRole('button', { name: 'Close' }).click()

  // Chain a tag action off the LLM node.
  await page.locator('.workflows-node-llm .workflows-node-add-button').click()
  await page.getByRole('button', { name: 'Add Tag', exact: true }).click()
  await expect(page.locator('.workflows-node-action')).toBeVisible()

  // Close the action node's auto-opened config panel before continuing.
  await page.getByRole('button', { name: 'Close' }).click()

  const workflowName = `e2e workflow ${Date.now()}`
  await page.getByRole('button', { name: 'Untitled workflow' }).click()
  await page.getByLabel('Workflow name').fill(workflowName)
  await page.getByLabel('Workflow name').press('Enter')

  await page.getByRole('button', { name: 'Save' }).click()
  // A successful save replaces the "new" placeholder id with the workflow's real id.
  await page.waitForURL(/\/workflows\/workflows\/(?!new$)[\w-]+$/)

  await page.goto('/workflows/workflows')
  const row = page.getByRole('row').filter({ hasText: workflowName })
  await expect(row).toBeVisible()
  await expect(row.getByText('Active')).toBeVisible()

  // Clean up via the UI's own delete flow — exercises it and leaves no test data behind.
  await row.getByRole('button', { name: 'Delete' }).click()
  await expect(row).toBeHidden()
})
