import { test, expect } from '@playwright/test'
import { login } from './support/auth'

test('run a saved workflow and see it succeed in the executions panel', async ({ page, request, baseURL }) => {
  await login(page)

  // Upload a target file directly over WebDAV — this spec's own fixture, not the app's UI.
  const token = await page.evaluate(() => {
    const key = Object.keys(localStorage).find((k) => k.startsWith('oc_oAuth.user:'))
    return key ? (JSON.parse(localStorage.getItem(key) as string).access_token as string) : ''
  })
  const authHeaders = { Authorization: `Bearer ${token}` }
  const davPath = `/e2e-run-workflow-${Date.now()}.txt`

  const uploadRes = await request.put(`${baseURL}/remote.php/dav/files/admin${davPath}`, {
    headers: authHeaders,
    data: 'Some file content to run the workflow against.'
  })
  expect(uploadRes.ok()).toBeTruthy()

  let workflowId = ''
  try {
    await page.goto('/workflows/workflows')
    await page.getByRole('button', { name: 'Add workflow' }).click()
    await page.waitForURL(/\/workflows\/workflows\/new$/)

    await page.getByRole('button', { name: 'Add trigger' }).click()
    await page.getByRole('button', { name: 'Manual Trigger', exact: true }).click()

    await page.locator('.workflows-node-trigger .workflows-node-add-button').click()
    await page.getByRole('button', { name: 'LLM Prompt', exact: true }).click()
    await page.locator('.workflows-node-llm').click()
    await page.getByLabel('Prompt', { exact: true }).fill('Summarize {{file.content}}')
    await page.getByRole('button', { name: 'Close' }).click()

    await page.locator('.workflows-node-llm .workflows-node-add-button').click()
    await page.getByRole('button', { name: 'Add Tag', exact: true }).click()
    await page.locator('.workflows-node-action').click()
    await page.getByLabel('Tag', { exact: true }).fill('e2e-run-workflow')
    await page.getByRole('button', { name: 'Close' }).click()

    await page.getByRole('button', { name: 'Save' }).click()
    await page.waitForURL(/\/workflows\/workflows\/(?!new$)[\w-]+$/)
    workflowId = page.url().split('/').pop() ?? ''

    await page.getByRole('button', { name: 'Executions' }).click()
    await page.getByLabel('File to run against (WebDAV path)').fill(davPath)
    await page.getByRole('button', { name: 'Run now' }).click()

    await expect(page.locator('.workflows-status-pill.is-active')).toBeVisible({ timeout: 15000 })
    await expect(page.getByText('This is a fake LLM response for testing.')).toBeVisible()
  } finally {
    await request.delete(`${baseURL}/remote.php/dav/files/admin${davPath}`, { headers: authHeaders })
    if (workflowId) {
      await request.delete(`${baseURL}/workflows/api/v1beta1/me/workflows/${workflowId}`, { headers: authHeaders })
    }
  }
})
