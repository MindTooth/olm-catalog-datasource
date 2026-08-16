# OLM catalog datasource Helm chart

This chart deploys the datasource on Kubernetes and OpenShift with a `restricted-v2` compatible pod security context.

## Prerequisites

Create a registry authentication Secret and a ConfigMap or Secret containing the approved `containers/image` `policy.json`. The chart deliberately does not render credentials or an insecure image policy.

## Install

    helm upgrade --install olm-catalog-datasource charts/olm-catalog-datasource -f values-openshift.yaml

`values-openshift.yaml` must set a released application image, at least one catalog source, and the existing policy object. Registry auth and refresh-token Secrets are optional references.

## Operations

- The Service remains internal by default. Set `route.enabled=true` only when external access is required.
- `/healthz` is used for startup and liveness; `/readyz` gates Service endpoints until a catalog refresh succeeds.
- `emptyDir` cache is the default. Enable persistence only when retaining registry layers across replacements is worth the storage cost.
- NetworkPolicy intentionally controls ingress only. Standard Kubernetes policy cannot safely represent a registry hostname allow-list; enforce egress in the cluster network layer.

## Security

The chart creates no Role, RoleBinding, SCC, or privileged init container. It disables service-account-token mounting, does not set a fixed UID/GID, uses a read-only root filesystem, and mounts only `/tmp` and the cache as writable paths.
