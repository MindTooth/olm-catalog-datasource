# OpenShift release changelogs

`GET /v1/openshift-releases/{channel}/updates` keeps Cincinnati authoritative for update eligibility. The installed release and only its direct, unconditional successors are update candidates; `arch` and `lag` keep their existing meanings.

That graph is not a complete changelog index. It can omit an accepted patch release even when a later release is a direct update target. To give Renovate a complete release-by-release changelog, the datasource also reads the public OpenShift release-controller stream that matches the requested architecture:

- `multi`: `4-stable-multi`
- `amd64`: `4-stable`
- `arm64`: `4-stable-arm64`
- `ppc64le`: `4-stable-ppc64le`
- `s390x`: `4-stable-s390x`

The release-controller's structured `/api/v1/releasestream/{stream}/tags` endpoint supplies accepted release names. For every accepted release in `(currentVersion, highest Cincinnati target]`, the datasource reads the release-controller's `/changelog?from={previous}&to={release}` Markdown. It does not derive versions, predecessor releases, or changelog text from version numbers or advisory names.

## Response and Renovate contract

Graph releases retain their Cincinnati version, digest, ordering, and advisory `changelogUrl`. If release-controller content is available, it replaces the short advisory summary in that release's `changelogContent`.

An accepted release that is absent from the Cincinnati candidates is included with its exact release-controller changelog and `isDeprecated: true`. Renovate does not select deprecated releases as updates, but its custom datasource changelog support retains their release notes when it renders the range from the installed version to the selected graph target. This keeps update decisions and their changelog history separate: a graph-gap release is visible in the PR notes without being proposed as an upgrade.

The installed release is not enriched. Releases outside the update range are not returned. A release-controller entry that cannot be read remains in the response (and remains deprecated if it is stream-only), but simply has no `changelogContent`; it never changes Cincinnati eligibility.

## Source, bounds, and failure handling

The release-controller changelog is the source of the detailed component change history. It is public Markdown generated for the release payload's real predecessor and release image. Each HTTP response is capped at 8 MiB to avoid unbounded reads. Requests share a five-second lookup budget and at most four per-release changelog requests run at once. The graph lookup remains governed by `openshiftTimeout` (30 seconds by default).

If stream discovery or an individual changelog request fails, the update response is still successful. Cincinnati targets remain present with their existing digest and advisory link. A graph target can retain its existing Red Hat advisory summary as a fallback when its release-controller changelog is unavailable.

## Cache behavior

Successful per-release controller changelogs are cached in memory by effective release-controller endpoint and exact `(previous release, release)` pair. Concurrent lookups for the same pair are coalesced. Entries are revalidated after six hours; if revalidation fails, the last successful content is used. Cold failures are not cached, so the next Renovate pass can retry. The cache is in-memory only and is cleared on restart.

The existing Red Hat advisory cache remains in place for fallback advisory summaries. It uses the same successful-cache, coalescing, stale-on-error, and cold-retry behavior.
