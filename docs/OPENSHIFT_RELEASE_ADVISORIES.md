# OpenShift release advisory summaries

`GET /v1/openshift-releases/{channel}/updates` keeps the OpenShift Cincinnati update graph authoritative for release eligibility. The installed release and only its direct, unconditional successors are returned; `arch` and `lag` keep their existing meanings. Advisory enrichment never adds releases or changes versions, digests, graph edges, or ordering.

For each returned target, the service preserves the graph node's `metadata.url` as `changelogUrl`. When that URL is a public Red Hat erratum (`RHSA`, `RHBA`, or `RHEA`), the service fetches that exact advisory and renders a concise `changelogContent` summary. Advisory identifiers are never inferred from OpenShift version numbers. This also means `.0` releases, minor-version transitions, and graph paths that skip an intermediate patch do not require synthetic predecessor versions.

## Source choice

The common source is the unauthenticated public Red Hat Customer Portal advisory page referenced by the graph. Red Hat's security-data feeds are useful for security advisories, but they are not a complete common source for bug-fix (`RHBA`) and enhancement (`RHEA`) advisories. The implementation therefore uses a bounded HTML parser over the public advisory's semantic sections rather than an undocumented application API.

Representative source fixtures are based on:

- `RHSA-2026:57365` — OpenShift Container Platform 4.22.11 security advisory.
- `RHBA-2025:23103` — OpenShift Container Platform 4.20.8 bug-fix advisory.
- `RHEA-2026:0449` — OpenShift Container Platform 4.22.0 enhancement advisory.

The parser extracts the advisory identifier, synopsis, issued/updated dates, type/severity, Fixes references and titles, CVEs, related Red Hat advisories, and OpenShift release-notes links. Repeated identifiers are deduplicated. Package/image inventories, digests, navigation and general portal boilerplate are not rendered.

Because the Customer Portal HTML is a presentation format rather than a documented structured API, its section structure is a maintenance dependency. If required sections such as `Synopsis` or `Type/Severity` cannot be recognized, enrichment is omitted instead of risking misleading content. `changelogUrl` remains available as the canonical link.

## Size and latency bounds

Each rendered advisory summary is limited to 6,000 bytes and is shortened only at entry boundaries. A shortened summary says so and links to the full advisory. Across one update response, embedded advisory content is capped at 24,000 bytes, with newer releases preferred; older summaries may fall back to a short link-only notice or, if no budget remains, only their existing `changelogUrl`.

The raw advisory response is bounded to 8 MiB because public OpenShift advisory pages can contain large image inventories even though those inventories are excluded from the rendered summary. Advisory enrichment has a five-second total budget per update lookup and at most four concurrent advisory requests. Graph lookup remains governed by `openshiftTimeout` (30 seconds by default).

## Cache behavior

Successful summaries are cached in memory by effective advisory source and advisory identifier. Concurrent requests for the same advisory are coalesced. Entries are revalidated after six hours because Red Hat advisories can be revised; if revalidation fails, the last successful cached summary is served. A cold cache fetches synchronously. A failed first fetch is not cached, so a later Renovate pass can retry. Restarting the service clears the cache. There is no background worker or persistent advisory cache.

## Completeness and consumer contract

The embedded text is deliberately labelled **Release advisory summary**. Each release object's `changelogContent` describes only the advisory associated with that release. It is not a complete source changelog and it does not claim to describe every component change or every intermediate release between the installed and target versions.

The Renovate consumer selects applicable release objects in `(currentVersion, newVersion]` and combines their release-level content newest-first. This producer therefore must not pre-aggregate current-to-target history into each target's `changelogContent`, because doing so duplicates older notes when Renovate combines multiple applicable releases.

A Cincinnati edge can legitimately connect `current -> target` while omitting an intermediate patch from the returned candidates. The target advisory can also refer to separate RPM/container advisories. This service does not add those intermediate versions to `releases`, because doing so would misrepresent update eligibility. Consequently, a graph gap is also a changelog-history gap unless the missing release is represented by an eligible release object supplied by the datasource.

If Renovate eventually needs complete current-to-target history independent of eligible update candidates, that is a separate producer-consumer extension. It requires explicit ordered history metadata from the producer and corresponding Renovate support for rendering that metadata separately from `releases`; per-release `changelogContent` must not be overloaded with cumulative history.
