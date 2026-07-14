# Contributing

Thank you for your interest in contributing to this project!

Please open an issue to discuss significant changes before submitting a pull request. For
small fixes, feel free to open a PR directly.

For development setup, coding standards, and pull request process, see the README in this
repository.

## Commit convention

This repository uses [Conventional Commits](https://www.conventionalcommits.org/). Every commit
message (and every PR title, since PRs are squash-merged) must follow:

```
<type>(<optional scope>): <description>

[optional body]

[optional footer(s)]
```

Common types: `feat`, `fix`, `docs`, `test`, `refactor`, `chore`, `ci`, `build`. Scope should be
`backend` or `frontend` when a change is limited to one side of the extension, e.g.:

```
feat(backend): add WebDAV-backed workflow store
fix(frontend): correct Vue Flow edge condition validation
```

`commitlint` (see `commitlint.config.js`) enforces this format locally via a `commit-msg` git hook
and in CI.

## Git workflow

- **Rebase policy**: Always rebase; never create merge commits.
- **Signed commits**: All commits **must** be PGP/GPG signed (`git commit -S -s`).
- **DCO sign-off**: Every commit needs a `Signed-off-by` line (`git commit -s`).
