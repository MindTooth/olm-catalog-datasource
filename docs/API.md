# HTTP API guide

This guide describes every HTTP endpoint exposed by
`olm-catalog-datasource`. It is useful in two different ways:

- **Renovate** should use the update endpoints. They always return the
  small `{"releases":[...]}` datasource format.
- **People and automation** can use the catalog-inspection endpoints to find
  packages, channels, bundle metadata, graph edges, and refresh status.

Catalog-inspection and Renovate endpoints accept `GET`. The two refresh-control
endpoints accept `POST` and only queue work; they never wait for a catalog pull
or parse to finish. Examples assume the service is listening locally on port
`8080`. Select the source containing the requested package: for example,
`redhat-v4.22` for OpenShift GitOps or `community-v4.22` for the community
Strimzi examples below.

## Before calling the API

Configure and start the service. A source ID is the value used in every URL;
it is not inferred from the image name.

```yaml
sources:
  - id: redhat-v4.22
    image: registry.redhat.io/redhat/redhat-operator-index:v4.22
    platform: linux/amd64
```

```fish
go run ./cmd/olm-catalog-datasource serve --config ./config.yaml --debug
```

The initial pull and catalog parse can take several minutes. Wait for readiness
before making a package request:

```fish
curl --fail-with-body http://localhost:8080/readyz
```

`200 OK` means at least one catalog has completed a successful refresh.

## Conventions

- Every successful manual-inspection response is JSON.
- `source` below means a configured source ID, for example `redhat-v4.22`.
- `package` means an Operator package name, for example
  `strimzi-kafka-operator`.
- A `404` means the source, package, or selected channel is unknown.
- A `503` means the configured source has not completed a successful refresh.
- The update endpoints return `422` for malformed or ambiguous current state.
- An empty Renovate release list is a valid answer: it means the catalog does
  not declare a graph-valid path for the requested state.

## Health and readiness

### `GET /healthz`

Use this for a basic process liveness probe. It returns `200` whenever the HTTP
server is running; it does not prove a catalog is loaded.

```fish
curl --fail-with-body http://localhost:8080/healthz
```

### `GET /readyz`

Use this for a Kubernetes readiness probe or before querying package data. It
returns `200` after at least one source refresh succeeds, otherwise `503`.

```fish
curl --fail-with-body http://localhost:8080/readyz
```

## Catalog discovery and status

### `GET /v1/catalogs`

Lists configured sources and their refresh state. This is the best first call
when you do not know the available source IDs.

```fish
curl --fail-with-body http://localhost:8080/v1/catalogs
```

Example shape:

```json
{
  "catalogs": [
    {
      "source": {
        "id": "redhat-v4.22",
        "image": "registry.redhat.io/redhat/redhat-operator-index:v4.22",
        "platform": "linux/amd64"
      },
      "available": true,
      "refreshing": false,
      "lastAttempt": "2026-08-14T12:00:00Z",
      "lastSuccess": "2026-08-14T12:00:00Z",
      "packageCount": 400
    }
  ]
}
```

### `GET /v1/catalogs/{source}/status`

Returns the same status object for one source. If the latest refresh failed
after a previous success, `available` remains true and `lastError` explains the
failure; the service continues serving the last good snapshot.

```fish
curl --fail-with-body \
  http://localhost:8080/v1/catalogs/redhat-v4.22/status
```

## Refresh control

Refreshing a catalog can pull and parse a large image, so refresh requests are
asynchronous. A successful response means the work was accepted, not that a new
snapshot is already available. Check the source's `/status` endpoint for
`refreshing`, `lastSuccess`, and `lastError`.

Both refresh endpoints are disabled until `refreshTokenFile` is configured.
When enabled, callers must send the file's trimmed contents as a Bearer token.
The token is read for every refresh request, so rotating a mounted Secret takes
effect without restarting the service.

### `POST /v1/refresh`

Queues every configured catalog. A source already queued or running is not
duplicated.

```fish
curl --fail-with-body -X POST \
  -H "Authorization: Bearer $(cat ./refresh-token)" \
  http://localhost:8080/v1/refresh
```

Example response:

```json
{
  "accepted": true,
  "sources": [
    {"source":"redhat-v4.22","state":"queued"},
    {"source":"community-v4.22","state":"running"}
  ]
}
```

### `POST /v1/catalogs/{source}/refresh`

Queues one configured source. It returns `404` for an unknown source.

```fish
curl --fail-with-body -X POST \
  -H "Authorization: Bearer $(cat ./refresh-token)" \
  http://localhost:8080/v1/catalogs/redhat-v4.22/refresh
```

The response is `202 Accepted` with `state` set to `queued` or `running`.
Requests with a missing or invalid token receive `401`; a missing or unreadable
token file makes refresh control unavailable with `503`. Restrict these
endpoints to trusted callers as a further layer of protection: they can cause
registry traffic and substantial CPU and memory use.

## Package discovery

### `GET /v1/catalogs/{source}/packages`

Lists packages from the latest source snapshot. This endpoint is for discovery,
not Renovate.

Parameters:

| Parameter | Meaning | Default |
| --- | --- | --- |
| `prefix` | Return names beginning with this value. | empty |
| `limit` | Maximum result count, between 1 and 1000. | 100 |

Find Strimzi packages:

```fish
curl --fail-with-body --get \
  http://localhost:8080/v1/catalogs/community-v4.22/packages \
  --data-urlencode 'prefix=strimzi' \
  --data-urlencode 'limit=20'
```

Each item includes the package name, its default channel, and the number of
known channels and bundles.

## Renovate datasource endpoints

These endpoints are deliberately small. They return Renovate's release-list
format:

```json
{"releases":[{"version":"value"}]}
```

### `GET /v1/catalogs/{source}/packages/{package}/updates`

Returns bundle versions reachable from the installed bundle through declared
`replaces`, `skips`, or `skipRange` edges. It does not propose every newer
version in the catalog.

Parameters:

| Parameter | Required | Meaning |
| --- | --- | --- |
| `currentVersion` | one of these | Installed operator version. |
| `currentBundle` | one of these | Installed bundle name; use it when a version is ambiguous. |
| `channel` | no | Channel to evaluate. Defaults to the package default channel. |
| `mode` | no | `reachable` walks the whole valid path; any other value returns direct successors only. |

```fish
curl --fail-with-body --get \
  http://localhost:8080/v1/catalogs/community-v4.22/packages/strimzi-kafka-operator/updates \
  --data-urlencode 'channel=strimzi-1.x' \
  --data-urlencode 'currentVersion=0.47.0' \
  --data-urlencode 'mode=reachable'
```

When the current release is not in the requested channel, the response is
successful but empty:

```json
{"releases":[]}
```

### `GET /v1/catalogs/{source}/packages/{package}/channel-updates`

Returns channels that the catalog explicitly permits the installed bundle to
enter. A channel name alone is never treated as an upgrade edge.

Parameters:

| Parameter | Required | Meaning |
| --- | --- | --- |
| `currentChannel` | yes | Current Subscription channel. |
| `currentVersion` | one of these | Installed version. |
| `currentBundle` | one of these | Installed bundle name. |
| `selection` | no | `next` returns the current channel plus the nearest valid target in the same versioned channel family when one exists; another value returns all valid targets. |

```fish
curl --fail-with-body --get \
  http://localhost:8080/v1/catalogs/community-v4.22/packages/strimzi-kafka-operator/channel-updates \
  --data-urlencode 'currentChannel=strimzi-1.x' \
  --data-urlencode 'currentVersion=0.47.0' \
  --data-urlencode 'selection=next'
```

The current channel is included in a non-empty response. Renovate can compare
the returned channel strings using an explicitly configured compatible
versioning scheme.

### `GET /v1/catalogs/{source}/packages/{package}/channel-releases`

This is the recommended endpoint for Renovate-managed `Subscription.spec.channel`
fields. It preserves OLM graph validation by requiring both the current channel
and an opaque current bundle-state token. Unlike `channel-updates`, it returns
only candidates (never the current channel), and each candidate includes the
selected terminal target bundle in Renovate's `digest` field.

Parameters:

| Parameter | Required | Meaning |
| --- | --- | --- |
| `currentChannel` | yes | Current Subscription channel. |
| `currentBundle` | one of these | Bundle-state token from the matching Renovate marker. |
| `currentVersion` | one of these | Installed version; less precise than a bundle name. |
| `selection` | no | `next` returns one strictly newer same-family target when possible. Any other value returns every graph-valid target. |

For example, this response means that the `gitops-1.21` channel can accept the
current bundle and has `openshift-gitops-operator.v1.21.2` as its selected graph
head:

```fish
curl --fail-with-body --get \
  http://localhost:8080/v1/catalogs/redhat-v4.22/packages/openshift-gitops-operator/channel-releases \
  --data-urlencode 'currentChannel=gitops-1.20' \
  --data-urlencode 'currentBundle=openshift-gitops-operator.v1.20.6' \
  --data-urlencode 'selection=next'
```

```json
{
  "releases": [
    {
      "version": "gitops-1.21",
      "digest": "openshift-gitops-operator.v1.21.2"
    }
  ]
}
```

`digest` is deliberately an opaque Renovate state token here, not an OCI image
digest. This configuration uses Renovate's regex-manager `currentDigest` and
`newDigest` fields to carry the exact FBC bundle name through the datasource
lookup and replacement.

#### Renovate configuration for graph-safe channel updates

Keep a Renovate-owned bundle-state marker directly above the `channel` field.
Seed it with the exact FBC bundle name, not merely the head of a channel. The
Subscription's `status.installedCSV` is useful evidence, but map it to the FBC
bundle name with the service's `/bundles` endpoint before enabling Renovate.

```yaml
# renovate: datasource=custom.olm-channel-graph depName=openshift-gitops-operator
# olm-catalog-datasource: bundleState=openshift-gitops-operator.v1.20.6
channel: gitops-1.20
```

The regex manager captures the channel as `currentValue`, the FBC bundle name
as `currentDigest`, and renders both into the endpoint URL. Its replacement
uses `newValue` and `newDigest` so the channel and state marker move together.

```json
{
  "customManagers": [
    {
      "customType": "regex",
      "managerFilePatterns": ["/\\.ya?ml$/"],
      "matchStrings": [
        "#[ \\t]*renovate:[ \\t]*datasource=custom\\.olm-channel-graph[ \\t]+depName=(?<depName>[^\\s]+)[ \\t]*\\n#[ \\t]*olm-catalog-datasource:[ \\t]*bundleState=(?<currentDigest>[^\\s]+)[ \\t]*\\n[ \\t]*channel:[ \\t]*(?<currentValue>(?<channelPrefix>[a-z0-9][a-z0-9-]*-)(?<channelVersion>\\d+\\.\\d+)(?<channelSuffix>\\.x)?)"
      ],
      "datasourceTemplate": "custom.olm-channel-graph",
      "versioningTemplate": "semver-coerced",
      "registryUrlTemplate": "http://olm-catalog-datasource:8080/v1/catalogs/redhat-v4.22/packages/{{{depName}}}/channel-releases?currentChannel={{{currentValue}}}&currentBundle={{{currentDigest}}}&selection=next",
      "autoReplaceStringTemplate": "# renovate: datasource=custom.olm-channel-graph depName={{{depName}}}\\n# olm-catalog-datasource: bundleState={{{newDigest}}}\\nchannel: {{{channelPrefix}}}{{{newValue}}}{{{channelSuffix}}}"
    }
  ],
  "customDatasources": {
    "olm-channel-graph": {
      "format": "json"
    }
  },
  "packageRules": [
    {
      "matchDatasources": ["custom.olm-channel-graph"],
      "matchPackageNames": ["openshift-gitops-operator"],
      "extractVersion": "^gitops-(?<version>\\d+\\.\\d+)$"
    },
    {
      "matchDatasources": ["custom.olm-channel-graph"],
      "matchPackageNames": ["strimzi-kafka-operator"],
      "extractVersion": "^strimzi-(?<version>\\d+\\.\\d+)\\.x$"
    }
  ]
}
```

`extractVersion` extracts the comparable numeric part for Renovate; the custom
replacement rebuilds the complete channel name using the captured prefix and
suffix. Add one package rule for every channel naming convention you manage.

The marker makes catalog graph resolution reproducible, but it cannot prove
that a cluster actually installed the predicted terminal bundle after merge.
Native Renovate has no cluster-status lookup field. For production, gate the
Renovate PR with a read-only cluster check that compares `bundleState` with the
Subscription's `status.installedCSV`; block the next channel change until they
match. A future cluster-aware endpoint can perform that check in the service
when it is given a namespace and Subscription name.

## Manual inspection endpoints

The following endpoints expose catalog metadata for people, scripts, and
troubleshooting. They are not Renovate datasource responses.

### `GET /v1/catalogs/{source}/packages/{package}/channels`

Lists every package channel in semantic suffix order. For example,
`gitops-1.6` comes before `gitops-1.10`; names without a comparable numeric
suffix, such as `latest`, use lexical ordering.

```fish
curl --fail-with-body \
  http://localhost:8080/v1/catalogs/redhat-v4.22/packages/openshift-gitops-operator/channels
```

Each channel contains:

- `name` and `deprecated` state;
- `entries`, the number of declared graph entries;
- `heads`, the terminal bundle or bundles found in that channel graph.

Add `include=entries` to include the full graph metadata in each channel:

```fish
curl --fail-with-body \
  'http://localhost:8080/v1/catalogs/redhat-v4.22/packages/openshift-gitops-operator/channels?include=entries'
```

### `GET /v1/catalogs/{source}/packages/{package}/bundles`

Lists known bundle metadata. Use `channel` to limit results to bundles directly
listed in one channel.

```fish
curl --fail-with-body \
  'http://localhost:8080/v1/catalogs/community-v4.22/packages/strimzi-kafka-operator/bundles?channel=strimzi-1.x'
```

Each bundle includes its name, package version, optional image reference, and
deprecation state.

### `GET /v1/catalogs/{source}/packages/{package}/graph`

Shows raw graph entries, including `replaces`, `skips`, and `skipRange`. Use it
to understand why a release is or is not eligible for an update.

All channels:

```fish
curl --fail-with-body \
  http://localhost:8080/v1/catalogs/community-v4.22/packages/strimzi-kafka-operator/graph
```

One channel:

```fish
curl --fail-with-body \
  'http://localhost:8080/v1/catalogs/community-v4.22/packages/strimzi-kafka-operator/graph?channel=strimzi-1.x'
```

Use the declared edges rather than entry order to reason about updates; graph
entries are metadata, not an ordered version list.

### `GET /v1/catalogs/{source}/packages/{package}/resolve`

Runs the same resolution logic as the Renovate endpoints, but provides a
human-oriented result. It returns `candidates`, `valid`, and a `reason` when
there is no valid path or the request is malformed.

Resolve bundle-version updates:

```fish
curl --fail-with-body --get \
  http://localhost:8080/v1/catalogs/community-v4.22/packages/strimzi-kafka-operator/resolve \
  --data-urlencode 'channel=strimzi-1.x' \
  --data-urlencode 'currentVersion=0.47.0' \
  --data-urlencode 'mode=reachable'
```

Resolve a channel transition by adding `kind=channel`:

```fish
curl --fail-with-body --get \
  http://localhost:8080/v1/catalogs/community-v4.22/packages/strimzi-kafka-operator/resolve \
  --data-urlencode 'kind=channel' \
  --data-urlencode 'currentChannel=strimzi-1.x' \
  --data-urlencode 'currentVersion=0.47.0' \
  --data-urlencode 'selection=next'
```

## Logging and safe operation

Every request is written as a structured access log with method, path, status,
response size, duration, and remote address. `serve --debug` (or `debug: true`
in the configuration) additionally records query strings, user agents, and
catalog refresh progress.

Mount registry credentials and a containers/image signature policy in
production. The service limits HTTP access to configured source IDs; it does
not allow callers to pull arbitrary image references.
