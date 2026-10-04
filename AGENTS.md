# AGENTS.md

## Project

Ratatoskr provides OpenShift release and OLM catalog version logic for Go applications and exposes it in forms suitable for Renovate.

Keep changes focused. Prefer existing Go libraries and in-process APIs over shelling out to external tools.

## Repository map

- `cmd/ratatoskr/` — CLI
- `pkg/catalog/` — reusable public OLM catalog package
- `internal/config/` — strict config parsing and source expansion
- `internal/service/` — HTTP API, refresh lifecycle, caching, Renovate responses
- `internal/openshift/` — OpenShift graph, release stream, advisories
- `charts/ratatoskr/` — Helm chart
- `docs/` — user and implementation contracts

## Core invariants

### OLM

- Treat the catalog graph as authoritative. A newer version is not automatically an update.
- Updates and channel changes must be justified by `replaces`, `skips`, or `skipRange`.
- Do not infer an installed bundle from the current channel; reject ambiguity instead of guessing.
- Keep `pkg/catalog` independent of HTTP/Renovate types and cluster access.
- Use Operator Framework and containers/image libraries; do not invoke `opm`, `oc-mirror`, Podman, or Docker.

### OpenShift

- Cincinnati is authoritative for update eligibility.
- Release-controller data may enrich release history but must not create selectable updates.
- Red Hat advisories provide changelog content.
- Changelog lookup failure must not remove valid Cincinnati candidates.

See `docs/OPENSHIFT_RELEASE_ADVISORIES.md` before changing this behavior.

### Configuration and service

- Keep configuration strict; unknown fields are errors.
- Preserve generated catalog sources versus explicit source overrides.
- Invalid reloads must not replace the last valid configuration.
- Keep refresh work bounded.
- Do not weaken registry authentication, TLS, signature-policy, or refresh-token behavior.

## Development

Use the Go version in `go.mod`. Run `gofmt`. Follow `godoclint` for exported declarations.

Prefer deterministic behavior, `context.Context` for cancellable I/O, and `%w` for wrapped errors. Avoid new dependencies unless they clearly improve on the standard library or existing dependencies.

For bug fixes, add a regression test when practical. Prefer local fixtures, `httptest`, `fstest`, or equivalent test doubles over live external services.

Treat exported `pkg/catalog` APIs, CLI flags, HTTP/JSON behavior, config keys/defaults, Helm values, and Renovate datasource semantics as compatibility surfaces. Update tests and docs when changing them.

## Verification

Run the narrowest relevant checks while iterating, then the applicable CI checks before finishing.

Go:

```sh
go mod verify
go test -race -tags=containers_image_openpgp ./...
CGO_ENABLED=0 go build -tags=containers_image_openpgp -trimpath -o /tmp/ratatoskr ./cmd/ratatoskr
golangci-lint run --build-tags=containers_image_openpgp
```

Do not omit `containers_image_openpgp` when validating the production build.

Helm:

```sh
helm lint --strict charts/ratatoskr --values .github/fixtures/helm/valid-values.yaml
helm lint --strict charts/ratatoskr --values .github/fixtures/helm/explicit-source-values.yaml
```

Render affected variants for template/schema changes. Chart changes require a version bump in `charts/ratatoskr/Chart.yaml`.

Do not claim a check passed unless it was actually run.

## Releases and commits

Releases are managed by semantic-release from `master`; `CHANGELOG.md` is generated. The Node/pnpm files are release tooling only.

When changing release behavior, inspect `release.config.cjs`, `package.json`, and `.github/workflows/release.yaml` together.

Use Angular-style Conventional Commits and PR titles: `type(scope): description` or `type: description`.

Supported types include `build`, `chore`, `ci`, `docs`, `feat`, `fix`, `perf`, `refactor`, `revert`, `style`, and `test`. Note that project-specific semantic-release rules make `build` and `refactor` patch releases.

Keep commits and PRs tightly scoped. Before finishing, inspect the diff, remove temporary artifacts, and report only verification actually performed.

## Security

Never commit credentials, tokens, keys, generated auth files, or other secrets. Do not turn development-only insecure settings into production defaults.
