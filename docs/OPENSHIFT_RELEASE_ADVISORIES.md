# OpenShift release changelogs

`GET /v1/openshift-releases/{channel}/updates` keeps Cincinnati authoritative for update eligibility. The installed release and only its direct, unconditional successors are update candidates; `arch` and `lag` keep their existing meanings.

That graph is not a complete changelog index. It can omit an accepted patch release even when a later release is a direct update target. To give Renovate a complete release-by-release changelog, the datasource also reads the public OpenShift release-controller stream that matches the requested architecture:

- `multi`: `4-stable-multi`
- `amd64`: `4-stable`
- `arm64`: `4-stable-arm64`
- `ppc64le`: `4-stable-ppc64le`
- `s390x`: `4-stable-s390x`

The release-controller's structured `/api/v1/releasestream/{stream}/tags` endpoint supplies accepted release names in `(currentVersion, highest Cincinnati target]`. It is used only to discover versions. The datasource does not fetch release-controller changelog bodies.

## Response and Renovate contract

Graph releases retain their Cincinnati version, digest, ordering, and advisory `changelogUrl`. Their `changelogContent` is parsed from the linked Red Hat errata page.

An accepted release that is absent from the Cincinnati candidates is included with `isDeprecated: true`. When that version has an advisory URL in the Cincinnati graph metadata, its `changelogUrl` points to the advisory and its `changelogContent` is parsed from that errata page. Renovate does not select deprecated releases as updates, but retains their available release notes when it renders the range from the installed version to the selected graph target.

The installed release is not enriched. Releases outside the update range are not returned. A stream-only release without a graph advisory URL remains in the response without `changelogContent`; no advisory ID is inferred from its version.

## Source, bounds, and failure handling

Red Hat errata pages are the only source of embedded changelog content. Advisory requests share a five-second lookup budget and at most four run at once. Embedded summaries are bounded per release and across the response. The graph lookup remains governed by `openshiftTimeout` (30 seconds by default).

If stream discovery or an individual advisory request fails, the update response is still successful. Cincinnati targets remain present with their existing digest and advisory link. A release without a successfully parsed advisory simply has no `changelogContent`.

## Cache behavior

Successful Red Hat advisory summaries are cached in memory for six hours. Concurrent lookups for the same advisory are coalesced. If revalidation fails, the last successful content is used. Cold failures are not cached, so the next Renovate pass can retry. The cache is cleared on restart.
