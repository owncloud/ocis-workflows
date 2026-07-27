import { test, expect } from '@playwright/test'
import { login } from './support/auth'

test('the "+" add-next button aligns consistently across node types', async ({ page }) => {
  await login(page)

  await page.goto('/workflows/workflows')
  await page.getByRole('button', { name: 'Add workflow' }).click()
  await page.waitForURL(/\/workflows\/workflows\/new$/)

  // Chain a trigger -> LLM -> action, matching build-workflow.spec.ts, so we get one
  // instance of each of the three node components that render the shared add-button.
  await page.getByRole('button', { name: 'Add trigger' }).click()
  await page.getByRole('button', { name: 'Manual Trigger', exact: true }).click()
  await expect(page.locator('.workflows-node-trigger')).toBeVisible()
  // Adding the node opens its Node Details panel automatically; close it before
  // continuing, otherwise its overlay covers the canvas and intercepts clicks below.
  await page.getByRole('button', { name: 'Close' }).click()

  await page.locator('.workflows-node-trigger .workflows-node-add-button').click()
  await page.getByRole('button', { name: 'LLM Prompt', exact: true }).click()
  await expect(page.locator('.workflows-node-llm')).toBeVisible()
  await page.getByRole('button', { name: 'Close' }).click()

  await page.locator('.workflows-node-llm .workflows-node-add-button').click()
  await page.getByRole('button', { name: 'Add Tag', exact: true }).click()
  await expect(page.locator('.workflows-node-action')).toBeVisible()
  await page.getByRole('button', { name: 'Close' }).click()

  // Adding a node schedules a re-fitted viewport via a 50ms-delayed `fitView` call (see
  // `fitViewSoon` in WorkflowBuilder.vue — it needs Vue Flow to measure the new node's DOM
  // first). Wait it out before measuring, otherwise these bounding-box reads can race that
  // delayed pan/zoom and pick up transient coordinates.
  await page.waitForTimeout(150)

  const centerOffsets: number[] = []

  for (const cardClass of ['workflows-node-trigger', 'workflows-node-llm', 'workflows-node-action']) {
    const card = page.locator(`.${cardClass}`)
    const cardBox = await card.boundingBox()
    const buttonBox = await card.locator('.workflows-node-add-button').boundingBox()
    expect(cardBox, `${cardClass} card should have a bounding box`).not.toBeNull()
    expect(buttonBox, `${cardClass} add-button should have a bounding box`).not.toBeNull()
    if (!cardBox || !buttonBox) continue

    // The button must be vertically centered on its own card, regardless of the
    // card's height or its border-radius shape (e.g. the trigger's pill-shaped left
    // corners must not throw off the button sitting on the right edge).
    const cardCenterY = cardBox.y + cardBox.height / 2
    const buttonCenterY = buttonBox.y + buttonBox.height / 2
    expect(
      Math.abs(buttonCenterY - cardCenterY),
      `${cardClass} add-button should be vertically centered on the card`
    ).toBeLessThanOrEqual(1)

    // The button must sit essentially at the card's right edge -- coordinated with Vue
    // Flow's own connection handle there -- rather than floating away from the card
    // with a visible, disconnected-looking gap.
    const buttonCenterX = buttonBox.x + buttonBox.width / 2
    const cardRightX = cardBox.x + cardBox.width
    const offsetFromEdge = buttonCenterX - cardRightX
    expect(
      Math.abs(offsetFromEdge),
      `${cardClass} add-button should sit right at the card's edge, not floating away from it`
    ).toBeLessThanOrEqual(6)

    centerOffsets.push(offsetFromEdge)
  }

  // And that offset must be identical (within a tight tolerance) across all three node
  // types, so the button can't silently drift out of sync between the three node
  // components again.
  const [first, ...rest] = centerOffsets
  for (const offset of rest) {
    expect(Math.abs(offset - first)).toBeLessThanOrEqual(1)
  }
})
