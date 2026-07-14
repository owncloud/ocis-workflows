import { test, expect } from '@playwright/test'
import { login } from './support/auth'

test('build and save a trigger -> LLM -> action workflow on the canvas', async ({ page }) => {
  await login(page)

  await page.goto('/workflows/workflows')
  await expect(page.getByRole('heading', { name: 'Workflows' })).toBeVisible()

  await page.getByRole('button', { name: 'New workflow' }).click()
  await page.waitForURL(/\/workflows\/workflows\/new$/)

  const workflowName = `e2e workflow ${Date.now()}`
  await page.getByLabel('Workflow name').fill(workflowName)

  await page.getByRole('button', { name: 'Add LLM step' }).click()
  await page.getByRole('button', { name: 'Add action' }).click()
  await expect(page.locator('.workflows-node-llm')).toBeVisible()
  await expect(page.locator('.workflows-node-action')).toBeVisible()

  await page.getByRole('button', { name: 'Save' }).click()
  // A successful save replaces the "new" placeholder id with the workflow's real id.
  await page.waitForURL(/\/workflows\/workflows\/(?!new$)[\w-]+$/)

  await page.goto('/workflows/workflows')
  await expect(page.getByRole('link', { name: workflowName })).toBeVisible()
})
