# Getting started

This guide takes you from an empty machine to a running datasource. It explains
the external pieces the service needs: access to a catalog registry, registry
credentials, an image signature policy, and writable cache and temporary
storage.

For endpoint details and further `curl` examples, see the [HTTP API guide](API.md).

## What you need

For local use:

- Go 1.26 or newer;
- network access to the catalog registry, for example `registry.redhat.io`;
- an account entitled to pull the desired Red Hat catalog image;
- Podman or another OCI-compatible way to log in to the registry;
- `curl` for testing the HTTP API.

For a pod deployment, the same registry access is required from the pod rather
than your laptop. The pod needs a mounted authentication file, a mounted
containers/image policy, and writable `/tmp` and cache directories.

## 1. Get the source and prepare Go modules

```fish
git clone https://github.com/MindTooth/olm-catalog-datasource.git
cd olm-catalog-datasource
git switch initial-implementation
go mod tidy
go test ./...
```

`go mod tidy` downloads the upstream Operator Framework libraries and creates
`go.sum` if it is not already present. Do this in a networked development or CI
environment before building the container image.

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

Copy the example and add the policy path and source you want to query:

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
# Required to enable POST /v1/refresh endpoints.
refreshTokenFile: ./refresh-token
sources:
  - id: community-v4.22
    image: registry.redhat.io/redhat/community-operator-index:v4.22
    platform: linux/amd64
```

`id` is a stable local name used in API URLs. You can configure more than one
catalog, including different OpenShift releases such as `v4.20` and `v4.22`.

The refresh endpoints are disabled until `refreshTokenFile` is set. Generate a
token, keep it out of source control, and restrict the file permissions:

```fish
openssl rand -base64 32 > refresh-token
chmod 600 refresh-token
```

### Apple Silicon and other non-Linux hosts

Most Red Hat catalog images are published as `linux/amd64`. The `platform`
field tells the registry library which image manifest to pull; it does not run
the catalog image. Set this when working on an Apple Silicon Mac:

```yaml
platform: linux/amd64
```

## 5. Run and verify locally

```fish
go run ./cmd/olm-catalog-datasource serve --config ./config.yaml --debug
```

### Updating configuration without restarting

`serve` checks the configuration file every five seconds by default. This works
with ordinary files and Kubernetes ConfigMap mounts, whose updates are applied
by atomically replacing symlinks. A valid change is applied as one unit; an
invalid change is logged and the last valid configuration and catalog snapshots
remain active.

Changes to `sources`, `refreshInterval`, `refreshTimeout`, `parseConcurrency`,
`signaturePolicy`, and `refreshTokenFile` take effect automatically. A new or
changed source is queued for an immediate refresh. Changing `listenAddress`
still requires a pod or process restart because the HTTP listener is already
bound.

To tune the check period or turn it off:

```fish
go run ./cmd/olm-catalog-datasource serve --config ./config.yaml \
  --config-reload-interval 10s

go run ./cmd/olm-catalog-datasource serve --config ./config.yaml \
  --config-reload-interval 0
```

The first catalog refresh can take several minutes. In a second terminal:

```fish
curl --fail-with-body http://localhost:8080/healthz
curl --fail-with-body http://localhost:8080/readyz
```

`/healthz` confirms that the process is running. `/readyz` returns `503` until
at least one catalog finishes loading, then returns `200`.

You can trigger an asynchronous refresh after changing external registry state
or when testing a catalog update. It is accepted immediately; inspect the
source status to see whether it has completed:

```fish
curl --fail-with-body -X POST \
  -H "Authorization: Bearer $(cat ./refresh-token)" \
  http://localhost:8080/v1/catalogs/community-v4.22/refresh

curl --fail-with-body \
  http://localhost:8080/v1/catalogs/community-v4.22/status
```

The configured token is required for refresh calls. Keep the endpoint internal
to the cluster or also protect it with ingress authentication and NetworkPolicy;
it can initiate expensive registry pulls.

Discover packages:

```fish
curl --fail-with-body --get \
  http://localhost:8080/v1/catalogs/community-v4.22/packages \
  --data-urlencode 'prefix=strimzi'
```

Query an upgrade path:

```fish
curl --fail-with-body --get \
  http://localhost:8080/v1/catalogs/community-v4.22/packages/strimzi-kafka-operator/updates \
  --data-urlencode 'channel=strimzi-1.x' \
  --data-urlencode 'currentVersion=0.47.0' \
  --data-urlencode 'mode=reachable'
```

An empty `{"releases":[]}` is a valid result: the selected channel does not
declare a usable path from that installed release.

## 6. Build and run a container

Build an image after completing `go mod tidy`:

```fish
podman build -t olm-catalog-datasource:dev -f Containerfile .
mkdir -p .cache
```

Run it with explicit mounts. The image runs as non-root, so keep the auth and
policy mounts read-only and provide writable `/tmp` and `OLM_CACHE_DIR`:

```fish
podman run --rm -p 8080:8080 \
  -v "$PWD/config.yaml:/etc/olm-catalog-datasource/config.yaml:ro,Z" \
  -v "$PWD/policy.json:/etc/containers/policy.json:ro,Z" \
  -v "$HOME/.config/containers/auth.json:/var/run/registry-auth/auth.json:ro,Z" \
  -v "$PWD/.cache:/var/cache/olm:Z" \
  --tmpfs /tmp:rw,size=1g \
  -e REGISTRY_AUTH_FILE=/var/run/registry-auth/auth.json \
  -e OLM_CACHE_DIR=/var/cache/olm \
  olm-catalog-datasource:dev
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
  name: olm-catalog-datasource
  labels:
    app: olm-catalog-datasource
spec:
  # The service does not call the Kubernetes API.
  automountServiceAccountToken: false
  securityContext:
    runAsNonRoot: true
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: datasource
      image: registry.example/olm-catalog-datasource:tag
      args: ["serve", "--config", "/etc/olm-catalog-datasource/config.yaml"]
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
        failureThreshold: 3
      livenessProbe:
        httpGet:
          path: /healthz
          port: http
        periodSeconds: 10
        timeoutSeconds: 1
        failureThreshold: 3
      volumeMounts:
        - name: config
          mountPath: /etc/olm-catalog-datasource
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
The readiness probe uses `/readyz`, which stays `503` until at least one catalog
has refreshed successfully. Kubernetes therefore keeps the pod out of Service
endpoints while an initial catalog pull is in progress, without killing it for a
slow registry or a transient refresh failure. The startup probe allows up to one
minute for the process to bind its listener before liveness and readiness checks
begin.

For a long-running production deployment, replace the cache `emptyDir` with a
PVC if preserving layer cache across pod replacement is important. Use a
Deployment rather than a bare Pod for lifecycle management. Restrict network
egress to the catalog registries and limit inbound access to Renovate and
approved operators.

## Troubleshooting checklist

| Symptom | Likely cause | What to check |
| --- | --- | --- |
| `no policy.json file found` | No containers/image policy is configured. | Create or mount a policy and set `signaturePolicy`. |
| `authentication required` or `unauthorized` | Missing/expired registry credentials or entitlement. | `REGISTRY_AUTH_FILE`, registry login, and Red Hat entitlement. |
| No matching Darwin/ARM manifest | The catalog does not publish your host platform. | Add `platform: linux/amd64`. |
| `/readyz` stays `503` | Refresh is still running or repeatedly failing. | Start with `--debug`; inspect the refresh error and `/v1/catalogs`. |
| `{"releases":[]}` | No graph-valid update path from that state. | Check `/channels`, `/graph`, and `/resolve`. |

## Next steps

- Configure Renovate using the update endpoint described in the [HTTP API guide](API.md).
- Add each supported OpenShift catalog as a separate source ID to compare
  release streams such as `v4.20` and `v4.22`.
- Mount an approved signature policy and a rotating registry-auth Secret before
  exposing the service to automated consumers.
