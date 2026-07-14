#!/usr/bin/env node
// Fails CI if any dependency (other than @ownclouders/* — ownCloud Web's own SDK, which is
// AGPL-3.0 and unavoidable for a Web extension) carries a copyleft license we haven't
// explicitly reviewed. Dual/OR-licensed packages are fine as long as one option is
// permissive (we simply elect that option) — only a bare GPL/AGPL/LGPL/MPL identifier with
// no alternative is treated as a violation.
import { execSync } from 'node:child_process'

const COPYLEFT = /^(A?GPL|LGPL|MPL)/i
const EXEMPT_SCOPE = '@ownclouders/'

const raw = execSync('pnpm licenses list --json', { encoding: 'utf8' })
const byLicense = JSON.parse(raw)

const violations = []
const unknowns = []

for (const [license, pkgs] of Object.entries(byLicense)) {
  const isDualLicensed = /\bOR\b/i.test(license)
  const isBareCopyleft = COPYLEFT.test(license.trim()) && !isDualLicensed

  for (const pkg of pkgs) {
    if (license === 'Unknown') {
      unknowns.push(pkg.name)
      continue
    }
    if (isBareCopyleft && !pkg.name.startsWith(EXEMPT_SCOPE)) {
      violations.push(`${pkg.name} (${license})`)
    }
  }
}

if (unknowns.length) {
  console.warn(`Warning: could not determine a license for: ${unknowns.join(', ')} — review manually.`)
}

if (violations.length) {
  console.error('Disallowed copyleft dependency licenses found:')
  for (const v of violations) console.error(`  - ${v}`)
  console.error(
    '\nGPL/AGPL/LGPL/MPL dependencies require explicit review before being introduced ' +
      `(the ${EXEMPT_SCOPE}* packages are exempt — they are ownCloud Web\'s own AGPL-3.0 SDK, ` +
      'required by definition for any Web extension).'
  )
  process.exit(1)
}

console.log('Dependency license check passed.')
