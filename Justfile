# Common local commands for the datasource.

default:
    @just --list

# Run the service with the local configuration.
run:
    go run -tags=containers_image_openpgp ./cmd/ratatoskr serve --config ./config.yaml

# Run the service with verbose request and refresh logs.
debug:
    go run -tags=containers_image_openpgp ./cmd/ratatoskr serve --config ./config.yaml --debug

# Download and verify Go dependencies.
deps:
    go mod download
    go mod verify

# Run the Go tests with the production build tag.
test:
    go test -tags=containers_image_openpgp ./...

# Run the same race and coverage checks as CI.
test-race: coverage

# Collect native Go coverage reports outside the checkout.
coverage:
    #!/usr/bin/env bash
    set -euo pipefail
    coverage_dir="$(mktemp -d)"
    printf 'Coverage output: %s\n' "$coverage_dir"
    go test -mod=readonly -count=1 -race -tags=containers_image_openpgp \
      -covermode=atomic -coverpkg=./... -coverprofile="$coverage_dir/coverage.out" ./...
    go tool cover -func="$coverage_dir/coverage.out" > "$coverage_dir/coverage.txt"
    go tool cover -html="$coverage_dir/coverage.out" -o "$coverage_dir/coverage.html"
    cat "$coverage_dir/coverage.txt"

# Vet the production build configuration.
vet:
    go vet -tags=containers_image_openpgp ./...

# Build the static binary in the temporary directory.
build:
    CGO_ENABLED=0 go build -tags=containers_image_openpgp -trimpath -o /tmp/ratatoskr ./cmd/ratatoskr

# Lint the chart with both CI values fixtures.
helm-lint:
    helm lint --strict charts/ratatoskr --values .github/fixtures/helm/valid-values.yaml
    helm lint --strict charts/ratatoskr --values .github/fixtures/helm/explicit-source-values.yaml
