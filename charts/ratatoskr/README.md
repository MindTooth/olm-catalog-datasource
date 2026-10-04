# ratatoskr Helm chart

This chart deploys the datasource on Kubernetes and OpenShift with a `restricted-v2` compatible pod security context.

## Prerequisites

Create a registry authentication Secret when required by the registry. For image signature policy, either reference an existing ConfigMap or Secret containing `policy.json`, or let the chart generate a policy for selected Red Hat operator index versions.

## Install

Install from the GitHub Pages Helm repository:

GitHub Pages must first deploy the `gh-pages` branch from the repository root;
this publishes the repository chart index.

    helm repo add mindtooth https://mindtooth.github.io/ratatoskr
    helm upgrade --install ratatoskr mindtooth/ratatoskr --version 0.3.3 -f values-openshift.yaml

Or install the same chart from GitHub Container Registry:

If the GHCR package is private, authenticate with a GitHub token that has
`read:packages` before pulling it:

    printf '%s' "$GHCR_TOKEN" | helm registry login ghcr.io --username YOUR_GITHUB_USERNAME --password-stdin

    helm upgrade --install ratatoskr oci://ghcr.io/mindtooth/charts/ratatoskr --version 0.3.3 -f values-openshift.yaml

For a checkout-based installation, use:

    helm upgrade --install ratatoskr charts/ratatoskr -f charts/ratatoskr/values-openshift.yaml

`values-openshift.yaml` must set a released application image, at least one
catalog channel or exact source, and a signature policy source. Registry auth
and refresh-token Secrets are optional references.

The common catalog configuration is:

    config:
      channels:
        - "4.22"

This creates the standard Red Hat, certified, and community sources. Set
`config.catalogs` to select a subset. `config.platform` defaults to
`linux/amd64`; `config.sources` supports exact replacements and custom sources.

`config.maxSnapshotAge` defaults to `24h` and must be positive. Failed datasource
refreshes retain complete in-memory results through this age; missing or expired
data returns `503`. Retention starts empty after a process restart. See the
[freshness policy](../../docs/CONFIGURATION.md#settings) for response metadata
and OpenShift lookup retention limits.
See the project [configuration guide](../../docs/CONFIGURATION.md) for the
normalization and precedence rules.

### Generated operator index policy

Instead of supplying an existing policy object, the chart can generate a restrictive `containers/image` policy that rejects images by default and permits the selected Red Hat operator index tags with `insecureAcceptAnything`:

    config:
      channels:
        - "4.22"
      # Empty selects redhat, certified, and community.
      catalogs: []

    signaturePolicy:
      operatorVersions:
        - "4.21"
        - "4.22"
      existingConfigMap: ""
      existingSecret: ""
      key: policy.json

Each version is converted to an exact `vX.Y` policy scope for the built-in operator index repositories selected by `config.catalogs`. `operatorVersions`, `existingConfigMap`, and `existingSecret` are mutually exclusive.

The chart version and application version are independent. The default image tag is the chart `appVersion` (`1.0.0` for chart `0.3.3`); override it with `image.tag`, or preferably pin an immutable `image.digest`.

## Operations

- The Service remains internal by default. Set `route.enabled=true` only when external access is required.
- `/healthz` is used for startup and liveness; `/readyz` requires a complete snapshot within `config.maxSnapshotAge` for every configured catalog. Readiness fails on the first failed probe; missing or expired data never causes liveness restarts.
- Each pod uses its own `emptyDir` cache by default. `persistence.enabled=true` requires `replicaCount=1` and `pdb.enabled=false`; the chart rejects a shared cache in multi-replica deployments.
- NetworkPolicy intentionally controls ingress only. Standard Kubernetes policy cannot safely represent a registry hostname allow-list; enforce egress in the cluster network layer.

## High availability

The chart defaults to `replicaCount: 2` and `pdb.enabled: true` with
`pdb.maxUnavailable: 1`. The OpenShift example inherits these defaults. For a
single-instance development installation, set both `replicaCount: 1` and
`pdb.enabled: false`.

Preferred pod anti-affinity uses this release's selector labels and
`kubernetes.io/hostname` to favor different nodes. It is a preference, so a
single-node cluster, a temporarily reduced node pool, or a rolling-update surge
can still schedule pods together. Production node-loss tolerance requires at
least two eligible nodes with enough CPU, memory and ephemeral storage, and
verification that ready replicas actually occupy different nodes. Existing
`nodeSelector`, `tolerations` and `affinity` values are rendered; a nonempty
`affinity` replaces the default, so include pod anti-affinity in custom rules
when node separation is needed. There is no required zone or minimum node count.

With two ready replicas, losing one pod or its node leaves the other Service
endpoint available while the Deployment replaces the failed pod. In-flight
connections to the failed pod can still fail, and node-failure detection and
endpoint propagation take time. A replacement stays unready until **all** its
configured catalogs have complete acceptable snapshots. Startup and liveness
check the HTTP listener, allowing slow initial pulls without restart loops.
A failed refresh keeps a replica ready while its retained datasets satisfy
`config.maxSnapshotAge`; expiry of any configured catalog removes it from the
Service, even if another catalog is fresh. This deliberately trades partial
catalog availability for predictable routing of all configured lookups.

The PDB allows one healthy pod eviction when both replicas are ready and blocks
eviction of the last healthy replica until another is ready. `AlwaysAllow`
permits eviction of unready pods so failed pods do not block a drain. Use the
Eviction API (for example `kubectl drain`); direct deletion, forced drains and
involuntary failures are not prevented by a PDB. Rolling updates use
`maxUnavailable: 0` and `maxSurge: 1`, keeping existing ready replicas until a
replacement becomes ready; allow capacity for the extra pod. The existing
one-hour progress deadline reports a stalled rollout if full initialization
takes longer; it does not restart pods.

### Interaction with datasource failure and retention

Last-known-good catalog snapshots and exact OpenShift lookup results are
**process-local memory**, not shared files. Each catalog refresh builds a private
snapshot and publishes it atomically only after a complete successful read.
Independent replicas cannot publish a peer's partially refreshed data, and a
restart loses only that replica's retained results. No shared storage or leader
election is required. The optional PVC is a registry cache, not snapshot
persistence; sharing it would add storage/access-mode constraints without
sharing last-known-good state.

Replicas refresh independently and may temporarily return different complete
snapshot generations, including during upstream outages. Responses are not
monotonic across replicas: a later request may see an older, still acceptable
snapshot. Inspect `X-Ratatoskr-Generated-At`, `X-Ratatoskr-Snapshot-Age-Seconds`
and per-pod catalog status to observe freshness. Manual refresh requests affect
only the receiving replica; scheduled refreshes run in every replica. Each
replica also performs its own registry pulls and OpenShift lookups, so budget
upstream traffic, memory and temporary storage accordingly.

Readiness covers configured catalogs, not every possible OpenShift query.
OpenShift retention is populated on demand for an exact lookup in each process;
one replica may have a fallback that another has never loaded or has evicted.
Such requests still return `503` when authoritative data cannot be produced.
A shared upstream outage beyond the age policy, or restarting all replicas
during an outage, can leave the whole service unavailable.

HA improves availability. The failure semantics from #80 and complete snapshot
retention from #81 protect correctness independently: missing or expired data
returns `503`, never a successful failed/partial release set. Keep Renovate's
`abortOnError: true` host rule as described in the
[API guide](../../docs/API.md); replicas do not replace that protection.

## Security

The chart creates no Role, RoleBinding, SCC, or privileged init container. It disables service-account-token mounting, does not set a fixed UID/GID, uses a read-only root filesystem, and mounts only `/tmp` and the cache as writable paths.
