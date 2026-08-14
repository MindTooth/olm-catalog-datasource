# OLM catalog datasource

`olm-catalog-datasource` is a native Go service for exposing OpenShift file-based
operator catalog updates to Renovate. It pulls catalog images through the upstream
Operator Framework image libraries; it does not execute `opm`, `oc-mirror`, or a
container client.

Start with [the getting-started guide](docs/GETTING_STARTED.md) for local,
container, and pod deployment instructions, including registry authentication
and signature-policy setup. See [the HTTP API guide](docs/API.md) for every
endpoint, parameter, response, and copy-ready `curl` example.

The service returns only versions connected to the current bundle by declared
`replaces`, `skips`, or `skipRange` edges. It deliberately does not treat every
newer catalog version as an upgrade.

## Status

This initial implementation supports native catalog refresh, graph-filtered
bundle-version responses, and graph-validated channel transitions. A channel
update needs the installed bundle identity or an unambiguous installed version;
the service deliberately refuses to guess.

## Configuration

```yaml
listenAddress: ":8080"
# Set true to include query strings and user agents in request logs.
# debug: true
refreshInterval: 6h
refreshTimeout: 30m
parseConcurrency: 2
# Mount an explicit containers/image policy in production.
# signaturePolicy: /etc/containers/policy.json
sources:
  - id: redhat-v4.22
    image: registry.redhat.io/redhat/redhat-operator-index:v4.22
    # Override the image platform when testing a Linux-only catalog from macOS.
    # platform: linux/amd64
  - id: certified-v4.22
    image: registry.redhat.io/redhat/certified-operator-index:v4.22
  - id: community-v4.22
    image: registry.redhat.io/redhat/community-operator-index:v4.22
```

Registry credentials are read from the standard containers/image locations. In
Fish, a mounted Docker config can be selected with:

```fish
set -gx REGISTRY_AUTH_FILE /var/run/registry-auth/auth.json
```

`platform` is optional. Set it when the catalog is available only for a
different platform than the host running the service, such as local testing on
an Apple Silicon Mac:

```yaml
sources:
  - id: community-v4.22
    image: registry.redhat.io/redhat/community-operator-index:v4.22
    platform: linux/amd64
```

## Run

```fish
go run ./cmd/olm-catalog-datasource serve --config ./config.yaml
```

Each HTTP request is logged with method, path, response status, response size,
duration, and remote address. Add `--debug` (or `debug: true` in the
configuration) to also log query strings and user agents, plus catalog refresh
progress:

```fish
go run ./cmd/olm-catalog-datasource serve --config ./config.yaml --debug
```

The server automatically reloads a changed configuration file every five
seconds, including Kubernetes ConfigMap mounts. Invalid updates leave the
last valid configuration active. Set `--config-reload-interval 0` to disable
this behavior; changing `listenAddress` still requires a restart.

For a one-off query, pass the same selection explicitly:

```fish
go run ./cmd/olm-catalog-datasource query \
  --image registry.redhat.io/redhat/community-operator-index:v4.20 \
  --platform linux/amd64 \
  --package strimzi-kafka-operator \
  --channel stable \
  --current-version 0.47.0
```

After the initial refresh:

```fish
curl --fail-with-body \
  'http://localhost:8080/v1/catalogs/redhat-v4.22/packages/openshift-gitops-operator/channel-releases?currentChannel=gitops-1.20&currentBundle=openshift-gitops-operator.v1.20.6&selection=next'
```

The response follows Renovate's minimal custom datasource shape:

```json
{"releases":[{"version":"gitops-1.21","digest":"openshift-gitops-operator.v1.21.2"}]}
```

If the installed release is not in the requested channel, the service returns
`{"releases":[]}` with HTTP 200. This means there is no valid update path;
it is not a catalog error.

## Renovate

```json
{
  "customDatasources": {
    "openshift-operators-v4-20": {
      "defaultRegistryUrlTemplate": "http://olm-catalog-datasource.example/v1/catalogs/redhat-v4.22/packages/{{packageName}}/updates?currentVersion={{currentValue}}&channel=gitops-1.20&mode=reachable",
      "format": "json"
    }
  }
}
```

Use explicit Renovate versioning, for example `semver` or `loose`.

## Channel upgrades

Channel names are not upgrade edges. The `channel-updates` endpoint returns a
target only when its channel graph has a `replaces`, `skips`, or `skipRange`
entry covering the installed bundle or version. For Renovate-managed channel
fields, use the graph-safe `channel-releases` endpoint with the companion
bundle-state marker described in the [API guide](docs/API.md#renovate-configuration-for-graph-safe-channel-updates):

```fish
olm-catalog-datasource channel-query \
  --image registry.redhat.io/redhat/redhat-operator-index:v4.20 \
  --package openshift-gitops-operator \
  --current-channel gitops-1.20 \
  --current-bundle openshift-gitops-operator.v1.20.6
```

```text
GET /v1/catalogs/redhat-v4.22/packages/openshift-gitops-operator/channel-updates
    ?currentChannel=gitops-1.20&currentBundle=openshift-gitops-operator.v1.20.6&selection=next

GET /v1/catalogs/redhat-v4.22/packages/openshift-gitops-operator/channel-releases
    ?currentChannel=gitops-1.20&currentBundle=openshift-gitops-operator.v1.20.6&selection=next
```

## Manual catalog inspection

These endpoints expose compact catalog metadata for manual lookup; unlike the
update endpoints, they are not Renovate datasource responses.

```text
GET /v1/catalogs
GET /v1/catalogs/{source}/status
POST /v1/refresh
POST /v1/catalogs/{source}/refresh
GET /v1/catalogs/{source}/packages?prefix=strimzi&limit=100
GET /v1/catalogs/{source}/packages/{package}/channels?include=entries
GET /v1/catalogs/{source}/packages/{package}/bundles?channel=stable
GET /v1/catalogs/{source}/packages/{package}/graph?channel=stable
GET /v1/catalogs/{source}/packages/{package}/resolve?channel=stable&currentVersion=1.0.0&mode=reachable
```

`/channels` includes every channel, its deprecation state, entry count, and
terminal graph bundle(s). `/resolve` returns candidates and a reason for an
invalid request or absent path; use `kind=channel` with `currentChannel` to
inspect a channel transition.

## Security

Use a mounted authentication file and an explicit containers/image signature
policy. The service does not disable TLS verification or accept unsigned images
by default. Do not expose it as an arbitrary image proxy: the HTTP API can query
only configured source IDs.

## Limitations

Catalog reachability is not a guarantee that an InstallPlan will succeed. Cluster
dependencies, admission controls, mirrors, and existing state can still block an
upgrade.

## Container

The `Containerfile` builds a static binary and runs it without a shell or
fixed root UID. Mount a writable `/tmp`, a cache volume through `OLM_CACHE_DIR`,
the configuration file, and registry authentication or policy files as needed.
