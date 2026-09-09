package openshift

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnrichChangelogsEightTargetsShareAggregateBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/")
		var fixes strings.Builder
		for i := range 100 {
			fmt.Fprintf(&fixes, `<li><a href="https://issues.redhat.com/browse/OCPBUGS-%d">OCPBUGS-%d</a> - %s</li>`, i, i, strings.Repeat("bounded release note ", 12))
		}
		fmt.Fprintf(w, `<html><body><h1>%s - Bug Fix Advisory</h1><h2>Synopsis</h2><p>release %s</p><h2>Type/Severity</h2><p>Bug Fix Advisory</p><h2>Fixes</h2><ul>%s</ul></body></html>`, id, id, fixes.String())
	}))
	t.Cleanup(server.Close)

	client := Client{ErrataURL: server.URL, HTTPClient: server.Client(), ChangelogCache: NewChangelogCache()}
	releases := make([]Release, 8)
	for i := range releases {
		id := fmt.Sprintf("RHBA-2026:%d", 200+i)
		releases[i] = Release{
			Version:      fmt.Sprintf("4.22.%d", i+1),
			ChangelogURL: "https://access.redhat.com/errata/" + id,
		}
	}
	client.enrichChangelogs(context.Background(), releases)

	total := 0
	shortened := false
	for _, release := range releases {
		total += len(release.ChangelogContent)
		if release.ChangelogContent != "" {
			if !strings.Contains(release.ChangelogContent, "https://access.redhat.com/errata/") {
				t.Fatalf("%s content lost canonical source link:\n%s", release.Version, release.ChangelogContent)
			}
			if strings.Contains(release.ChangelogContent, "Summary omitted") || strings.Contains(release.ChangelogContent, "Summary shortened") {
				shortened = true
			}
		}
	}
	if total > maxAggregateChangelogBytes {
		t.Fatalf("aggregate changelog content = %d, want <= %d", total, maxAggregateChangelogBytes)
	}
	if !shortened {
		t.Fatal("expected explicit shortening or omission notice")
	}
	if releases[len(releases)-1].ChangelogContent == "" {
		t.Fatal("newest release lost its advisory summary")
	}
}
