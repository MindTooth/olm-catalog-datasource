# Common local commands for the datasource.

default:
    @just --list

# Run the service with the local configuration.
run:
    go run -tags=containers_image_openpgp ./cmd/olm-catalog-datasource serve --config ./config.yaml

# Run the service with verbose request and refresh logs.
debug:
    go run -tags=containers_image_openpgp ./cmd/olm-catalog-datasource serve --config ./config.yaml --debug

# Download and verify Go dependencies.
deps:
    go mod download
    go mod verify

# Run the Go tests with the production build tag.
test:
    go test -tags=containers_image_openpgp ./...

# Run the same race and coverage checks as CI.
test-race:
    go test -race -tags=containers_image_openpgp -coverprofile=coverage.out ./...

# Vet the production build configuration.
vet:
    go vet -tags=containers_image_openpgp ./...

# Build the static binary in the temporary directory.
build:
    CGO_ENABLED=0 go build -tags=containers_image_openpgp -trimpath -o /tmp/olm-catalog-datasource ./cmd/olm-catalog-datasource

# Lint the chart with both CI values fixtures.
helm-lint:
    helm lint --strict charts/olm-catalog-datasource --values .github/fixtures/helm/valid-values.yaml
    helm lint --strict charts/olm-catalog-datasource --values .github/fixtures/helm/explicit-source-values.yaml
