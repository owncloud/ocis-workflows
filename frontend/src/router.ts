export const appId = 'workflows'

/**
 * Path for the workflow builder view.
 *
 * Built as a plain path for use with hard navigation (`window.location.assign`) rather
 * than Vue Router's `push`/`router-link`: navigating into this app's `:id` sub-route via
 * the host Web app's shared router instance throws inside Web's own persistent-layout
 * sidebar code (a resource-injection assumption baked into components that render on every
 * route change, unrelated to this app's logic). A hard navigation reliably works, at the
 * cost of a full page reload — acceptable for switching between the workflow list and
 * builder. Revisit once the underlying Web-router interaction is root-caused upstream.
 */
export function builderPath(id: string): string {
  return `/${appId}/workflows/${encodeURIComponent(id)}`
}

export function listPath(): string {
  return `/${appId}/workflows`
}
