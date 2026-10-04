# AGENTS.md

## Project

Ratatoskr provides OpenShift release and OLM catalog version logic for Go
applications and exposes that data in forms suitable for Renovate.

Keep the implementation small and focused. Prefer existing Go libraries and
in-process APIs over shelling out to external tools.

## Repository structure

- `cmd/ratatoskr/` — CLI entry point and commands.
- `pkg/catalog/` — reusable public Go package for loading, parsing, and resolving OLM file-based catalogs.
- `internal/config/` — strict configuration parsing, validation, defaults, and source expansion.
- `internal/service/` — HTTP API, catalog refresh lifecycle, caching, and Renovate-compatible responses.
- `internal/openshift/` — OpenShift release graph, release-stream, and advisory handling.
- `charts/ratatoskr/` — Helm chart for Kubernetes and OpenShift deployments.
- `docs/` — API, configuration, deployment, and implementation contracts.
- `.github/workflows/` — CI and release pipelines.

## Architecture and behavior

Preserve these contracts unless a change explicitly requires modifying them.

### OLM catalogs

- Treat the catalog graph as authoritative for update reachability.
- A numerically or semantically newer bundle is not automatically an update.
- Updates must be justified by `replaces`, `skips`, or `skipRange`.
- Channel changes must likewise be graph-proven.
- Do not infer the installed bundle from the current channel name.
- If an installed version is ambiguous, require the bundle identity rather than guessing.
- Keep `pkg/catalog` reusable. It must not depend on the HTTP service or Renovate-specific response types.
- Use the upstream Operator Framework and containers/image libraries rather than invoking `opm`, `oc-mirror`, Podman, Docker, or another container CLI.

### OpenShift releases

- Cincinnati remains authoritative for update eligibility.
- The release-controller stream may supplement release history but must not make otherwise-ineligible releases selectable updates.
- Red Hat advisory data supplies embedded changelog content.
- Keep update eligibility separate from changelog enrichment.
- Changelog lookup failure must not remove otherwise-valid Cincinnati update candidates.

See `docs/OPENSHIFT_RELEASE_ADVISORIES.md` before changing this behavior.

### Configuration and service

- Configuration parsing is strict. Unknown fields should remain errors rather than being silently ignored.
- Preserve the distinction between generated catalog sources and explicit source overrides.
- Invalid configuration reloads must not replace the last valid configuration.
- Keep refresh operations bounded and avoid unbounded concurrency.
- Do not weaken registry authentication, TLS, signature-policy, or refresh-token behavior for convenience.

## Go changes

Use the Go version declared in `go.mod` and format Go code with `gofmt`.

Exported declarations must have useful Go documentation where required by
`godoclint`.

Prefer standard-library facilities where practical, explicit error propagation
and `%w` when wrapping errors, `context.Context` for cancellable I/O,
deterministic ordering for externally visible collections, and small changes
that preserve existing public behavior.

Do not add dependencies unless they provide clear value over the standard
library or existing dependencies.

For bug fixes, add a regression test that demonstrates the previously incorrect
behavior when practical. Tests should be deterministic and should normally use
local fixtures, `httptest`, `fstest`, or equivalent test doubles instead of
live external services.

## Public contracts

Treat these as public compatibility surfaces:

- exported APIs under `pkg/catalog`;
- CLI commands and flags;
- HTTP routes, query parameters, status codes, and JSON response shapes;
- configuration keys and defaults;
- Helm values and rendered resource behavior;
- Renovate custom-datasource response semantics.

When changing one of these surfaces, update or add tests, update the relevant
documentation, and preserve backward compatibility unless the task explicitly
calls for a breaking change.

Relevant documentation includes `README.md`, `docs/GETTING_STARTED.md`,
`docs/CONFIGURATION.md`, `docs/API.md`, and
`docs/OPENSHIFT_RELEASE_ADVISORIES.md`.

## Verification

Use the narrowest relevant checks while iterating, then run the applicable CI
checks before declaring the change complete.

For Go changes:

```sh
go mod download
go mod verify
go test -race -tags=containers_image_openpgp -coverprofile=coverage.out ./...
CGO_ENABLED=0 go build -tags=containers_image_openpgp -trimpath -o /tmp/ratatoskr ./cmd/ratatoskr
golangci-lint run --build-tags=containers_image_openpgp
```

Do not omit `containers_image_openpgp` when verifying the production build
configuration.

For Helm changes, at minimum run:

```sh
helm lint --strict charts/ratatoskr --values .github/fixtures/helm/valid-values.yaml
helm lint --strict charts/ratatoskr --values .github/fixtures/helm/explicit-source-values.yaml
```

Render and inspect affected variants when changing templates, values, schema
validation, persistence, PDB, Route, or OpenShift behavior. CI enforces a Helm
chart version bump when chart contents change; update
`charts/ratatoskr/Chart.yaml` accordingly.

For container changes, verify the `Containerfile` build with Podman or an
equivalent OCI builder when available.

Do not claim a check passed unless it was actually run.

## Releases and generated files

Application releases are managed by semantic-release from `master`.
`CHANGELOG.md` is generated by semantic-release; do not manually author
release entries as part of ordinary changes.

The Node/pnpm files exist for release tooling. Ratatoskr itself is a Go
application; do not introduce Node into the runtime architecture.

When modifying release behavior, inspect `release.config.cjs`, `package.json`,
and `.github/workflows/release.yaml` together.

## Commits and pull requests

Use Angular-style Conventional Commits for commit messages and pull request
titles.

Use `type(scope): description` or `type: description`, with a type such as
`build`, `chore`, `ci`, `docs`, `feat`, `fix`, `perf`, `refactor`,
`revert`, `style`, or `test`. Use a breaking-change marker or footer when
the change is intentionally breaking.

The semantic-release configuration has project-specific release rules. In
particular, `build` and `refactor` changes can trigger patch releases.

Do not assume that a syntactically valid type appears in release notes. When
changing semantic-release rules, keep release triggering and release-note
visibility consistent.

Keep commits and pull requests tightly scoped. Do not mix unrelated cleanup,
dependency updates, formatting churn, or refactoring into a focused change.

Before finishing, inspect the final diff, remove temporary debugging code and
generated test artifacts, verify relevant tests and documentation, and summarize
what changed and what verification was actually performed.

## Security

Never commit registry credentials, refresh tokens, private keys, generated auth
files, or other secrets.

Do not replace secure production defaults with development shortcuts. Local
insecure signature-policy examples are for development only and must not become
production defaults.
