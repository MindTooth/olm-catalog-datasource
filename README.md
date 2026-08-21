# OLM catalog datasource

`olm-catalog-datasource` is a native Go service for exposing OpenShift cluster
releases and file-based operator catalog updates to Renovate. Cluster releases
come from the official OpenShift update graph. Operator catalogs are pulled
through the upstream Operator Framework image libraries; the service does not
execute `opm`, `oc-mirror`, or a container client.

Start with [the getting-started guide](docs/GETTING_STARTED.md) for local,
container, and pod deployment instructions, including registry authentication
and signature-policy setup. The [configuration guide](docs/CONFIGURATION.md)
defines channel expansion, defaults, and exact-source overrides. See the
[HTTP API guide](docs/API.md) for every endpoint, parameter, response, and
copy-ready `curl` example.

The service returns only versions connected to the current bundle by declared
`replaces`, `skips`, or `skipRange` edges. It deliberately does not treat every
newer catalog version as an upgrade.

## Status

The service supports direct OpenShift cluster-release updates, native catalog
refresh, graph-filtered bundle-version responses, and graph-validated channel
transitions. A channel update needs the installed bundle identity or an
unambiguous installed version; the service deliberately refuses to guess.

## Configuration

```yaml
channels:
  - "4.22"
```

This creates the `redhat-v4.22`, `certified-v4.22`, and `community-v4.22`
sources with the standard Red Hat images. `v4.22` is also accepted. Catalog
images default to `linux/amd64` on every host.

Registry credentials are read from the standard containers/image locations. In
Fish, a mounted Docker config can be selected with:

```fish
set -gx REGISTRY_AUTH_FILE /var/run/registry-auth/auth.json
```

Select a catalog subset, change the global platform, or retain exact source
control when needed:

```yaml
platform: linux/amd64
channels: [v4.22]
catalogs: [redhat, community]
sources:
  - id: community-v4.22
    image: mirror.example.com/community-operator-index:v4.22
```

The explicit source replaces the generated source with the same ID. See the
[configuration guide](docs/CONFIGURATION.md) for precedence and migration.

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

OpenShift cluster releases require only a channel and the installed version:

```json
{
  "customDatasources": {
    "openshift-releases": {
      "defaultRegistryUrlTemplate": "http://olm-catalog-datasource.example/v1/openshift-releases/{{packageName}}/updates?currentVersion={{currentValue}}&arch=multi&lag=1",
      "format": "json"
    }
  }
}
```

Use the channel, for example `stable-4.21`, as `packageName`. The response
contains the installed release and only its direct unconditional successors.
`lag=1` withholds the newest successor; the installed release is never removed.

Operator catalog releases use the catalog endpoints:

```json
{
  "customDatasources": {
    "openshift-operators-v4-22": {
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

Use a mounted registry-authentication file, an explicit containers/image
signature policy, and a refresh-token file. Refresh endpoints are disabled
until `refreshTokenFile` is configured; callers must then send its contents as
an `Authorization: Bearer` token. The service does not disable TLS verification
or accept unsigned images by default. Do not expose it as an arbitrary image
proxy: the HTTP API can query only configured source IDs.

## Limitations

Catalog reachability is not a guarantee that an InstallPlan will succeed. Cluster
dependencies, admission controls, mirrors, and existing state can still block an
upgrade.

## Container

The `Containerfile` builds a static binary and runs it without a shell or
fixed root UID. Mount a writable `/tmp`, a cache volume through `OLM_CACHE_DIR`,
the configuration file, and registry authentication or policy files as needed.
