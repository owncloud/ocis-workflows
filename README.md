# oCIS Workflows

[![License](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)

oCIS Workflows is a full-stack extension for [ownCloud Infinite Scale (oCIS)](https://github.com/owncloud/ocis)
that lets end users define their own automated AI-powered workflows on their files — for
example "when I upload a PDF to /Invoices, summarize it with an LLM and tag it" — without
writing any code. Users build workflows visually (trigger → LLM step → actions) using a
node-graph editor, and the extension runs them on demand, on a schedule, or in reaction to
file activity.

It ships as two parts:

- **`backend/`** — a Go sidecar service that stores workflow definitions, runs them, and talks to
  oCIS only through its standard public APIs (WebDAV, Graph, the OIDC identity provider, and the
  `sse` notification stream) — no NATS dependency, no changes to oCIS core.
- **`frontend/`** — a Vue 3 extension for ownCloud Web providing the workflow builder UI, built
  with [Vue Flow](https://vueflow.dev).

LLM calls are made directly by the backend against a configured OpenAI-compatible
`LLM_ENDPOINT` — there is no external LLM proxy service or dependency.

## Getting Started

### Prerequisites

- [Docker](https://www.docker.com/) and [Docker Compose](https://docs.docker.com/compose/)
- [pnpm](https://pnpm.io/installation) (check `frontend/package.json`'s `packageManager` field)
- [Go](https://go.dev/) (check `backend/go.mod` for the required version)
- An OpenAI-compatible LLM endpoint reachable from Docker for local testing (e.g.
  [Ollama](https://ollama.com) on the host — the default `LLM_ENDPOINT` in `docker-compose.yml`
  points at `host.docker.internal:11434`).

### Development Environment

```bash
git clone https://github.com/LukasHirt/ocis-workflows.git
cd ocis-workflows

# frontend, build in watch mode
cd frontend && pnpm install && pnpm build:w &

# backend, run locally
cd backend && go run ./cmd/workflows server
```

Add to `/etc/hosts`:
```
127.0.0.1 host.docker.internal
```

Start the development stack (oCIS + Traefik):
```bash
docker compose up
```

Open [https://host.docker.internal:9200](https://host.docker.internal:9200) (default login: admin/admin).

### Testing

```bash
# backend
cd backend && go test ./...                    # unit
go test -tags=e2e ./tests/e2e/...               # e2e, requires the stack above running

# frontend
cd frontend && pnpm test:unit                   # unit
pnpm test:e2e                                   # e2e, requires the stack above running
```

Every feature or fix in this repo ships with e2e coverage on both the backend and frontend,
driven against the real docker-compose stack rather than mocks.

### Production Build

```bash
cd frontend && pnpm build         # output in frontend/dist/, deploy via WEB_ASSET_APPS_PATH
cd backend && go build ./cmd/workflows   # or docker build -f docker/Dockerfile .
```

## Documentation

- [Web Extension System Documentation](https://owncloud.dev/clients/web/extension-system/)
- [Web App Deployment Guide](https://owncloud.dev/services/web/#web-apps)
- [Vue Flow Documentation](https://vueflow.dev)

## Support

- [GitHub Issues](https://github.com/LukasHirt/ocis-workflows/issues) — bug reports, feature
  requests, and questions
- info@hirt.cz

See [SUPPORT.md](SUPPORT.md) for details.

## Contributing

We welcome contributions! Please read the [Contributing Guidelines](CONTRIBUTING.md)
and our [Code of Conduct](CODE_OF_CONDUCT.md) before getting started.

### Workflow

- **Rebase Early, Rebase Often!** We use a rebase workflow. Always rebase on the target branch before submitting a PR.
- **Conventional Commits**: enforced via `commitlint`, see [CONTRIBUTING.md](CONTRIBUTING.md).
- **Signed Commits**: All commits **must** be PGP/GPG signed.
- **DCO Sign-off**: Every commit must carry a `Signed-off-by` line: `git commit -s -S -m "..."`.
- **GitHub Actions Policy**: Workflows may only use actions that are created by GitHub
  (`actions/*`) or verified in the GitHub Marketplace, pinned to a full commit SHA.

## Security

**Do not open a public GitHub issue for security vulnerabilities.**

See [SECURITY.md](SECURITY.md) for how to report one privately.

## License

This project is licensed under the [Apache-2.0](LICENSE) license, copyright Lukas Hirt.
