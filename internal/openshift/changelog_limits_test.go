package openshift

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnrichChangelogsBoundsAggregateContentNewestFirst(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/")
		var fixes strings.Builder
		for i := range 80 {
			fmt.Fprintf(&fixes, `<li><a href="https://issues.redhat.com/browse/OCPBUGS-%d">OCPBUGS-%d</a> - %s</li>`, i, i, strings.Repeat("title ", 20))
		}
		fmt.Fprintf(w, `<html><body><h1>%s - Bug Fix Advisory</h1><h2>Synopsis</h2><p>release %s</p><h2>Type/Severity</h2><p>Bug Fix Advisory</p><h2>Fixes</h2><ul>%s</ul></body></html>`, id, id, fixes.String())
	}))
	t.Cleanup(server.Close)

	client := Client{ErrataURL: server.URL, HTTPClient: server.Client(), ChangelogCache: NewChangelogCache()}
	releases := make([]Release, 6)
	for i := range releases {
		id := fmt.Sprintf("RHBA-2026:%d", 100+i)
		releases[i] = Release{Version: fmt.Sprintf("4.22.%d", i+1), ChangelogURL: "https://access.redhat.com/errata/" + id}
	}
	client.enrichChangelogs(context.Background(), releases)

	total := 0
	for _, release := range releases {
		total += len(release.ChangelogContent)
	}
	if total > maxAggregateChangelogBytes {
		t.Fatalf("aggregate changelog content = %d, want <= %d", total, maxAggregateChangelogBytes)
	}
	if releases[len(releases)-1].ChangelogContent == "" || !strings.Contains(releases[len(releases)-1].ChangelogContent, "RHBA-2026:105") {
		t.Fatalf("newest release lost full summary: %q", releases[len(releases)-1].ChangelogContent)
	}
	if releases[0].ChangelogContent != "" && !strings.Contains(releases[0].ChangelogContent, "Summary omitted") {
		t.Fatalf("oldest release should be link-only or absent after aggregate limit: %q", releases[0].ChangelogContent)
	}
}
