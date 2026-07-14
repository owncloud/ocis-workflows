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

  // Chain an LLM step off the trigger's "+" handle.
  await page.locator('.workflows-node-trigger .workflows-node-add-button').click()
  await page.getByRole('button', { name: 'LLM Prompt', exact: true }).click()
  await expect(page.locator('.workflows-node-llm')).toBeVisible()

  // Configure the LLM node via its Node Details panel.
  await page.locator('.workflows-node-llm').click()
  await page.getByLabel('Prompt', { exact: true }).fill('Summarize this file in three bullet points.')
  await page.getByRole('button', { name: 'Close' }).click()

  // Chain a tag action off the LLM node.
  await page.locator('.workflows-node-llm .workflows-node-add-button').click()
  await page.getByRole('button', { name: 'Add Tag', exact: true }).click()
  await expect(page.locator('.workflows-node-action')).toBeVisible()

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
})
