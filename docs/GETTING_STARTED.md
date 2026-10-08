# Getting started

This guide takes you from an empty machine to a running datasource. It explains
the external pieces the service needs: access to a catalog registry, registry
credentials, an image signature policy, and writable cache and temporary
storage.

For configuration details, see the [configuration guide](CONFIGURATION.md).
For endpoint details and further `curl` examples, see the [HTTP API guide](API.md).

## What you need

For local use:

- the Go version specified in `go.mod`;
- network access to the catalog registry, for example `registry.redhat.io`;
- an account entitled to pull the desired Red Hat catalog image;
- Podman or another OCI-compatible way to log in to the registry;
- `curl` for testing the HTTP API.

For a pod deployment, the same registry access is required from the pod rather
than your laptop. The pod needs a mounted authentication file, a mounted
containers/image policy, and writable `/tmp` and cache directories.

## 1. Get the source and prepare Go modules

```fish
git clone https://github.com/MindTooth/ratatoskr.git
cd ratatoskr
go mod download
go mod verify
go test -tags=containers_image_openpgp ./...
```

`go mod download` verifies that the committed module definition can be
resolved without rewriting it. Run this in a networked development or CI
environment before building the container image.

### Inspect test coverage

With [just](https://just.systems/) installed, run `just coverage` (or
`just test-race`) to run the production-tag race tests and generate native Go
reports. Without just, run these commands in Bash or another POSIX shell:

```sh
(
  set -e
  coverage_dir="$(mktemp -d)"
  printf 'Coverage output: %s\n' "$coverage_dir"
  go test -mod=readonly -count=1 -race -tags=containers_image_openpgp \
    -covermode=atomic -coverpkg=./... -coverprofile="$coverage_dir/coverage.out" ./...
  go tool cover -func="$coverage_dir/coverage.out" > "$coverage_dir/coverage.txt"
  go tool cover -html="$coverage_dir/coverage.out" -o "$coverage_dir/coverage.html"
  cat "$coverage_dir/coverage.txt"
)
```

Open `coverage.html` from the printed temporary directory to inspect untested
code. The raw profile and function report are alongside it; reports stay outside
the checkout. CI publishes the whole-module total and function report in the
**Go test** job summary, with all three files in the run's **Artifacts** section
for 14 days after successful tests. Cross-package coverage includes code executed
by tests in other module packages, including the CLI in the overall total.
Individual test-binary percentages are not per-package coverage reports.

Coverage measures statement execution, not assertion quality or branch
correctness. Test changed contracts and relevant error cases, reproduce bug
fixes when practical, and cover concurrency or cancellation when affected.
Prefer local fixtures/test doubles and explain material testing limitations in
the PR. Use the PR diff and native reports for review; no numeric coverage
threshold is enforced.

## 2. Authenticate to the catalog registry

Red Hat catalog images require registry credentials. Log in with an entitled
Red Hat account and make the resulting auth file explicit:

```fish
mkdir -p "$HOME/.config/containers"
podman login --authfile "$HOME/.config/containers/auth.json" registry.redhat.io
set -gx REGISTRY_AUTH_FILE "$HOME/.config/containers/auth.json"
```

`REGISTRY_AUTH_FILE` is understood by the upstream containers/image library.
It is preferable to relying on an implicit Docker or Podman configuration,
especially in containers and CI.

Do not add `auth.json` to source control. It contains registry credentials.

## 3. Provide an image signature policy

The library refuses to pull images without a containers/image policy. In
production, mount the signature policy approved by your platform security team
and configure its path in `config.yaml`.

For a **local experiment only**, the following policy accepts unsigned content
from the Red Hat operator catalog repositories you listed while rejecting every
other image:

```json
{
  "default": [{ "type": "reject" }],
  "transports": {
    "docker": {
      "registry.redhat.io/redhat/redhat-operator-index": [
        { "type": "insecureAcceptAnything" }
      ],
      "registry.redhat.io/redhat/certified-operator-index": [
        { "type": "insecureAcceptAnything" }
      ],
      "registry.redhat.io/redhat/community-operator-index": [
        { "type": "insecureAcceptAnything" }
      ]
    }
  }
}
```

Save it as `policy.json` in the project directory. This does **not** disable
TLS, but it does skip signature verification for that one repository. Do not
use it in production. Use a verified policy and immutable image digests when
your environment requires supply-chain verification.

## 4. Create a catalog configuration

Copy the example and add the policy path and catalog channel you want to query:

```fish
cp config.example.yaml config.yaml
```

Minimal local configuration:

```yaml
listenAddress: ":8080"
debug: true
refreshInterval: 6h
refreshTimeout: 30m
parseConcurrency: 2
signaturePolicy: ./policy.json
# Required to enable POST /v2/catalogs/refresh endpoints.
refreshTokenFile: ./refresh-token
channels:
  - "4.22"
```

This creates the Red Hat, certified, and community catalogs for version `4.22`.
Use their v2 URLs as `redhat/4.22`, `certified/4.22`, and `community/4.22`.
`v4.22` is also accepted in API paths. Quote versions without the prefix so
YAML preserves the full value. Select multiple catalog versions by adding
channels, and use `catalogs` when only a subset of the three standard catalogs
is needed. The stable generated source IDs remain available through
`/v2/sources` when an exact-source request is required.

The refresh endpoints are disabled until `refreshTokenFile` is set. Generate a
token, keep it out of source control, and restrict the file permissions:

```fish
openssl rand -base64 32 > refresh-token
chmod 600 refresh-token
```

### Platform selection

Most Red Hat catalog images are published as `linux/amd64`. The `platform`
field tells the registry library which image manifest to pull; it does not run
the catalog image. `linux/amd64` is the default even when the service runs on
Apple Silicon or another platform, so no local-host override is normally
needed. A global or per-source override remains available:

```yaml
platform: linux/arm64
```

## 5. Run and verify locally

```fish
go run ./cmd/ratatoskr serve --config ./config.yaml --debug
```

### Updating configuration without restarting

`serve` checks the configuration file every five seconds by default. This works
with ordinary files and Kubernetes ConfigMap mounts, whose updates are applied
by atomically replacing symlinks. A valid change is applied as one unit; an
invalid change is logged and the last valid configuration and catalog snapshots
remain active.

Changes to `channels`, `catalogs`, `sources`, `platform`, refresh settings,
`parseConcurrency`, `signaturePolicy`, `refreshTokenFile`, and OpenShift graph
settings take effect automatically. A new or changed resolved source is queued
for an immediate refresh. Changing `listenAddress` still requires a pod or
process restart because the HTTP listener is already bound.

To tune the check period or turn it off:

```fish
go run ./cmd/ratatoskr serve --config ./config.yaml \
  --config-reload-interval 10s

go run ./cmd/ratatoskr serve --config ./config.yaml \
  --config-reload-interval 0
```

The first catalog refresh can take several minutes. In a second terminal:

```fish
curl --fail-with-body http://localhost:8080/healthz
curl --fail-with-body http://localhost:8080/readyz
```

`/healthz` confirms that the process is running. `/readyz` returns `503` until
every configured catalog has loaded a complete snapshot within `maxSnapshotAge`.

You can trigger an asynchronous refresh after changing external registry state
or when testing a catalog update. It is accepted immediately; inspect the
source status to see whether it has completed:

```fish
curl --fail-with-body -X POST \
  -H "Authorization: Bearer $(cat ./refresh-token)" \
  http://localhost:8080/v2/catalogs/community/4.22/refresh

curl --fail-with-body \
  http://localhost:8080/v2/catalogs/community/4.22
```

The configured token is required for refresh calls. Keep the endpoint internal
to the cluster or also protect it with ingress authentication and NetworkPolicy;
it can initiate expensive registry pulls.

Discover packages:

```fish
curl --fail-with-body --get \
  http://localhost:8080/v2/catalogs/community/4.22/packages \
  --data-urlencode 'prefix=strimzi'
```

Query an upgrade path:

```fish
curl --fail-with-body --get \
  http://localhost:8080/v2/catalogs/community/4.22/packages/strimzi-kafka-operator/updates \
  --data-urlencode 'operatorChannel=strimzi-1.x' \
  --data-urlencode 'currentVersion=1.0.0' \
  --data-urlencode 'mode=reachable'
```

Configure the [required Renovate fail-safe host rule](../README.md#renovate)
with `abortOnError: true` before using these endpoints as custom datasources.
Lookup failures return `503`; Renovate must abort the run on these failures.

An empty `{"releases":[]}` is a valid result: the selected channel does not
declare a usable path from that installed release.

## 6. Build and run a container

Build an image after downloading and verifying the modules:

```fish
podman build -t ratatoskr:dev -f Containerfile .
mkdir -p .cache
```

Run it with explicit mounts. The image runs as non-root, so keep the auth and
policy mounts read-only and provide writable `/tmp` and `OLM_CACHE_DIR`:

```fish
podman run --rm -p 8080:8080 \
  -v "$PWD/config.yaml:/etc/ratatoskr/config.yaml:ro,Z" \
  -v "$PWD/policy.json:/etc/containers/policy.json:ro,Z" \
  -v "$HOME/.config/containers/auth.json:/var/run/registry-auth/auth.json:ro,Z" \
  -v "$PWD/.cache:/var/cache/olm:Z" \
  --tmpfs /tmp:rw,size=1g \
  -e REGISTRY_AUTH_FILE=/var/run/registry-auth/auth.json \
  -e OLM_CACHE_DIR=/var/cache/olm \
  ratatoskr:dev
```

Use a persistent cache volume for routine operation. The registry client can
reuse image layers through `OLM_CACHE_DIR`; the service still parses the latest
catalog into memory after each scheduled refresh.

## 7. Run as a Kubernetes or OpenShift pod

Create external objects before the workload:

- a ConfigMap holding `config.yaml`;
- a Secret holding the registry `auth.json`;
- a Secret holding the refresh token;
- a ConfigMap or Secret holding the signature `policy.json`;
- an `emptyDir` for `/tmp` and a PVC or `emptyDir` for the cache.

Example commands (adapt names and namespaces):

```sh
kubectl create configmap olm-catalog-config --from-file=config.yaml
kubectl create configmap olm-catalog-policy --from-file=policy.json
kubectl create secret generic olm-registry-auth --from-file=auth.json=/path/to/auth.json
kubectl create secret generic olm-refresh-token --from-file=token=./refresh-token
```

Use a Secret rather than a ConfigMap for any policy file that contains private
registry information. The example policy above has no secret material and can
be a ConfigMap.

Minimal Pod manifest:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: ratatoskr
  labels:
    app: ratatoskr
spec:
  # The service does not call the Kubernetes API.
  automountServiceAccountToken: false
  securityContext:
    runAsNonRoot: true
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: datasource
      image: registry.example/ratatoskr:tag
      args: ["serve", "--config", "/etc/ratatoskr/config.yaml"]
      ports:
        - containerPort: 8080
          name: http
      env:
        - name: REGISTRY_AUTH_FILE
          value: /var/run/registry-auth/auth.json
        - name: OLM_CACHE_DIR
          value: /var/cache/olm
      securityContext:
        allowPrivilegeEscalation: false
        readOnlyRootFilesystem: true
        capabilities:
          drop: ["ALL"]
      # The listener is the startup and liveness signal. Catalog refreshes can
      # take minutes, so they must not cause a container restart.
      startupProbe:
        httpGet:
          path: /healthz
          port: http
        periodSeconds: 2
        timeoutSeconds: 1
        failureThreshold: 30
      readinessProbe:
        httpGet:
          path: /readyz
          port: http
        periodSeconds: 5
        timeoutSeconds: 1
        failureThreshold: 1
      livenessProbe:
        httpGet:
          path: /healthz
          port: http
        periodSeconds: 10
        timeoutSeconds: 1
        failureThreshold: 3
      volumeMounts:
        - name: config
          mountPath: /etc/ratatoskr
          readOnly: true
        - name: policy
          mountPath: /etc/containers
          readOnly: true
        - name: registry-auth
          mountPath: /var/run/registry-auth
          readOnly: true
        - name: refresh-token
          mountPath: /var/run/olm-refresh-token
          readOnly: true
        - name: cache
          mountPath: /var/cache/olm
        - name: tmp
          mountPath: /tmp
  volumes:
    - name: config
      configMap:
        name: olm-catalog-config
    - name: policy
      configMap:
        name: olm-catalog-policy
    - name: registry-auth
      secret:
        secretName: olm-registry-auth
    - name: refresh-token
      secret:
        secretName: olm-refresh-token
    - name: cache
      emptyDir: {}
    - name: tmp
      emptyDir: {}
```

This manifest is compatible with OpenShift's default `restricted-v2` SCC. Do
not set `runAsUser` or `fsGroup` to a fixed value: OpenShift supplies values
from the namespace's allocated UID and FSGroup ranges. The image and mounted
writable paths must therefore work with an arbitrary non-root UID. The
`emptyDir` volumes provide the writable `/tmp` and cache paths; ConfigMaps and
Secrets remain read-only. `RuntimeDefault` seccomp, disabled privilege
escalation, and dropped capabilities are explicit defense-in-depth settings.

The probes intentionally have different meanings. The startup and liveness
probes use `/healthz`, which succeeds whenever the HTTP listener is serving.
The readiness probe uses `/readyz`, which stays `503` until every configured
catalog has a complete snapshot within `maxSnapshotAge`. Kubernetes therefore
keeps the pod out of Service endpoints while an initial catalog pull is in
progress, without killing it for a
slow registry or a transient refresh failure. The startup probe allows up to one
minute for the process to bind its listener before liveness and readiness checks
begin.

For production, use the [Helm chart](../charts/ratatoskr/README.md), which defaults
to two replicas, a PodDisruptionBudget and preferred node anti-affinity. The bare
Pod above is a minimal deployment example. Keep separate `emptyDir` caches for
HA: last-known-good snapshots are process-local memory, and a shared PVC does
not preserve them. The chart supports an optional registry-cache PVC only with
one replica and the PDB disabled. Restrict network egress to the catalog
registries and limit inbound access to Renovate and approved operators.

## Troubleshooting checklist

| Symptom | Likely cause | What to check |
| --- | --- | --- |
| `no policy.json file found` | No containers/image policy is configured. | Create or mount a policy and set `signaturePolicy`. |
| `authentication required` or `unauthorized` | Missing/expired registry credentials or entitlement. | `REGISTRY_AUTH_FILE`, registry login, and Red Hat entitlement. |
| No matching image manifest | The catalog does not publish the configured platform. | Check the global or per-source `platform`; the default is `linux/amd64`. |
| `/readyz` stays `503` | Refresh is still running or repeatedly failing. | Start with `--debug`; inspect the refresh error and `/v2/catalogs`. |
| `{"releases":[]}` | No graph-valid update path from that state. | Inspect the package with `?include=channels,graph` and verify the installed bundle state. |

## Next steps

- Configure Renovate using the update endpoint described in the [HTTP API guide](API.md).
- Add each supported OpenShift catalog version to `channels` to compare release
  streams such as `v4.20` and `v4.22`.
- Mount an approved signature policy and a rotating registry-auth Secret before
  exposing the service to automated consumers.
