# OLM catalog datasource Helm chart

This chart deploys the datasource on Kubernetes and OpenShift with a `restricted-v2` compatible pod security context.

## Prerequisites

Create a registry authentication Secret and a ConfigMap or Secret containing the approved `containers/image` `policy.json`. The chart deliberately does not render credentials or an insecure image policy.

## Install

Install from the GitHub Pages Helm repository:

GitHub Pages must first deploy the `gh-pages` branch from the repository root;
this publishes the repository chart index.

    helm repo add mindtooth https://mindtooth.github.io/olm-catalog-datasource
    helm upgrade --install olm-catalog-datasource mindtooth/olm-catalog-datasource --version 0.2.1 -f values-openshift.yaml

Or install the same chart from GitHub Container Registry:

If the GHCR package is private, authenticate with a GitHub token that has
`read:packages` before pulling it:

    printf '%s' "$GHCR_TOKEN" | helm registry login ghcr.io --username YOUR_GITHUB_USERNAME --password-stdin

    helm upgrade --install olm-catalog-datasource oci://ghcr.io/mindtooth/charts/olm-catalog-datasource --version 0.2.1 -f values-openshift.yaml

For a checkout-based installation, use:

    helm upgrade --install olm-catalog-datasource charts/olm-catalog-datasource -f charts/olm-catalog-datasource/values-openshift.yaml

`values-openshift.yaml` must set a released application image, at least one
catalog channel or exact source, and the existing policy object. Registry auth
and refresh-token Secrets are optional references.

The common catalog configuration is:

    config:
      channels:
        - "4.22"

This creates the standard Red Hat, certified, and community sources. Set
`config.catalogs` to select a subset. `config.platform` defaults to
`linux/amd64`; `config.sources` supports exact replacements and custom sources.
See the project [configuration guide](../../docs/CONFIGURATION.md) for the
normalization and precedence rules.

The chart version and application version are independent. The default image tag is the chart `appVersion` (`1.0.0` for chart `0.2.1`); override it with `image.tag`, or preferably pin an immutable `image.digest`.

## Operations

- The Service remains internal by default. Set `route.enabled=true` only when external access is required.
- `/healthz` is used for startup and liveness; `/readyz` gates Service endpoints until a catalog refresh succeeds.
- `emptyDir` cache is the default. Enable persistence only when retaining registry layers across replacements is worth the storage cost.
- NetworkPolicy intentionally controls ingress only. Standard Kubernetes policy cannot safely represent a registry hostname allow-list; enforce egress in the cluster network layer.

## Security

The chart creates no Role, RoleBinding, SCC, or privileged init container. It disables service-account-token mounting, does not set a fixed UID/GID, uses a read-only root filesystem, and mounts only `/tmp` and the cache as writable paths.
