package openshift

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestParseAdvisoryHTMLRepresentativeTypes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		file       string
		url        string
		id         string
		typeName   string
		severity   string
		issueCount int
		cveCount   int
		relatedID  string
	}{
		{name: "security", file: "rhsa-2026-57365.html", url: "https://access.redhat.com/errata/RHSA-2026:57365", id: "RHSA-2026:57365", typeName: "Security Advisory", severity: "Important", issueCount: 2, cveCount: 2, relatedID: "RHSA-2026:57361"},
		{name: "bug fix", file: "rhba-2025-23103.html", url: "https://access.redhat.com/errata/RHBA-2025:23103", id: "RHBA-2025:23103", typeName: "Bug Fix Advisory", issueCount: 2, relatedID: "RHBA-2025:23101"},
		{name: "enhancement", file: "rhea-2026-0449.html", url: "https://access.redhat.com/errata/RHEA-2026:0449", id: "RHEA-2026:0449", typeName: "Product Enhancement Advisory", issueCount: 1, relatedID: "RHEA-2026:5486"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, err := os.Open("testdata/" + tc.file)
			if err != nil {
				t.Fatal(err)
			}
			defer fixture.Close()

			summary, err := parseAdvisoryHTML(fixture, tc.url)
			if err != nil {
				t.Fatal(err)
			}
			if summary.ID != tc.id || summary.Type != tc.typeName || summary.Severity != tc.severity {
				t.Fatalf("summary identity = %#v", summary)
			}
			if len(summary.Issues) != tc.issueCount || len(summary.CVEs) != tc.cveCount {
				t.Fatalf("issues = %d, CVEs = %d", len(summary.Issues), len(summary.CVEs))
			}
			if len(summary.Related) != 1 || summary.Related[0].ID != tc.relatedID {
				t.Fatalf("related = %#v", summary.Related)
			}
			if len(summary.ReleaseDocs) != 1 || !strings.Contains(summary.ReleaseDocs[0].URL, "/release_notes/") {
				t.Fatalf("release docs = %#v", summary.ReleaseDocs)
			}
			markdown := renderAdvisoryMarkdown(summary, maxChangelogBytes)
			for _, want := range []string{"### Release advisory summary", tc.id, tc.typeName, "Red Hat advisory", "not a complete source changelog"} {
				if !strings.Contains(markdown, want) {
					t.Fatalf("markdown missing %q:\n%s", want, markdown)
				}
			}
			if strings.Contains(markdown, "sha256:") {
				t.Fatalf("markdown contains image inventory:\n%s", markdown)
			}
		})
	}
}

func TestParseAdvisoryHTMLRejectsChangedStructure(t *testing.T) {
	_, err := parseAdvisoryHTML(strings.NewReader(`<html><body><h1>RHBA-2025:23103</h1><p>layout changed</p></body></html>`), "https://access.redhat.com/errata/RHBA-2025:23103")
	if err == nil {
		t.Fatal("parseAdvisoryHTML() succeeded without required sections")
	}
}

func TestAdvisoryIDFromURL(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want string
		ok   bool
	}{
		{url: "https://access.redhat.com/errata/RHSA-2026:57365", want: "RHSA-2026:57365", ok: true},
		{url: "https://access.redhat.com/errata/RHBA-2025:23103/", want: "RHBA-2025:23103", ok: true},
		{url: "https://access.redhat.com/errata/RHEA-2026:0449", want: "RHEA-2026:0449", ok: true},
		{url: "https://access.redhat.com/errata/RHSA-2026:57365?utm_source=" + strings.Repeat("x", 10_000), want: "RHSA-2026:57365", ok: true},
		{url: "https://example.com/errata/RHSA-2026:57365"},
		{url: "http://access.redhat.com/errata/RHSA-2026:57365"},
		{url: "https://access.redhat.com/not-errata/RHSA-2026:57365"},
	} {
		got, ok := advisoryIDFromURL(tc.url)
		if got != tc.want || ok != tc.ok {
			t.Errorf("advisoryIDFromURL(%q) = %q, %v; want %q, %v", tc.url, got, ok, tc.want, tc.ok)
		}
	}
}

func TestEnrichChangelogsCachesAndCoalesces(t *testing.T) {
	body := fixtureBody(t, "rhsa-2026-57365.html")
	var requests atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/RHSA-2026:57365" {
			t.Errorf("path = %q", got)
		}
		if requests.Add(1) == 1 {
			close(started)
		}
		<-release
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	client := Client{ErrataURL: server.URL, HTTPClient: server.Client(), ChangelogCache: NewChangelogCache()}
	const advisoryURL = "https://access.redhat.com/errata/RHSA-2026:57365"
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			releases := []Release{{Version: "4.22.11", ChangelogURL: advisoryURL}}
			client.enrichChangelogs(context.Background(), releases)
			if !strings.Contains(releases[0].ChangelogContent, "RHSA-2026:57365") {
				t.Errorf("changelogContent = %q", releases[0].ChangelogContent)
			}
		}()
	}
	<-started
	close(release)
	wg.Wait()

	releases := []Release{{Version: "4.22.11", ChangelogURL: advisoryURL}}
	client.enrichChangelogs(context.Background(), releases)
	if got := requests.Load(); got != 1 {
		t.Fatalf("advisory requests = %d, want 1", got)
	}
}

func TestEnrichChangelogsStripsAdvisoryURLQuery(t *testing.T) {
	body := fixtureBody(t, "rhsa-2026-57365.html")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	const canonical = "https://access.redhat.com/errata/RHSA-2026:57365"
	releases := []Release{{Version: "4.22.11", ChangelogURL: canonical + "?tracking=" + strings.Repeat("x", maxChangelogBytes*2)}}
	client := Client{ErrataURL: server.URL, HTTPClient: server.Client(), ChangelogCache: NewChangelogCache()}
	client.enrichChangelogs(context.Background(), releases)

	if len(releases[0].ChangelogContent) > maxChangelogBytes {
		t.Fatalf("changelogContent length = %d", len(releases[0].ChangelogContent))
	}
	if !strings.Contains(releases[0].ChangelogContent, canonical) {
		t.Fatalf("canonical advisory URL missing:\n%s", releases[0].ChangelogContent)
	}
	if strings.Contains(releases[0].ChangelogContent, "tracking=") {
		t.Fatalf("advisory query leaked into changelogContent:\n%s", releases[0].ChangelogContent)
	}
}

func TestChangelogCacheRevalidatesAndUsesStaleOnFailure(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch requests.Add(1) {
		case 1:
			_, _ = w.Write([]byte(advisoryHTML("RHBA-2025:23103", "first synopsis")))
		case 2:
			_, _ = w.Write([]byte(advisoryHTML("RHBA-2025:23103", "revised synopsis")))
		default:
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(server.Close)

	cache := NewChangelogCache()
	client := Client{ErrataURL: server.URL, HTTPClient: server.Client(), ChangelogCache: cache}
	const id = "RHBA-2025:23103"
	const advisoryURL = "https://access.redhat.com/errata/RHBA-2025:23103"

	first, err := client.changelog(context.Background(), id, advisoryURL)
	if err != nil || !strings.Contains(first, "first synopsis") {
		t.Fatalf("first = %q, err = %v", first, err)
	}
	cached, err := client.changelog(context.Background(), id, advisoryURL)
	if err != nil || cached != first || requests.Load() != 1 {
		t.Fatalf("cached = %q, requests = %d, err = %v", cached, requests.Load(), err)
	}

	key := strings.TrimRight(server.URL, "/") + "\x00" + id
	cache.mu.Lock()
	entry := cache.entries[key]
	entry.fetchedAt = time.Now().Add(-changelogCacheTTL - time.Minute)
	cache.entries[key] = entry
	cache.mu.Unlock()

	revised, err := client.changelog(context.Background(), id, advisoryURL)
	if err != nil || !strings.Contains(revised, "revised synopsis") || requests.Load() != 2 {
		t.Fatalf("revised = %q, requests = %d, err = %v", revised, requests.Load(), err)
	}

	cache.mu.Lock()
	entry = cache.entries[key]
	entry.fetchedAt = time.Now().Add(-changelogCacheTTL - time.Minute)
	cache.entries[key] = entry
	cache.mu.Unlock()

	stale, err := client.changelog(context.Background(), id, advisoryURL)
	if err != nil || stale != revised || requests.Load() != 3 {
		t.Fatalf("stale = %q, requests = %d, err = %v", stale, requests.Load(), err)
	}
}

func TestChangelogFailureModesAreNonFatalToEnrichment(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{name: "http error", handler: func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusBadGateway) }},
		{name: "unknown structure", handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html><h1>RHSA-2026:57365</h1></html>`)) }},
		{name: "oversized", handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", maxErrataBytes+1))) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()
			releases := []Release{{Version: "4.22.11", ChangelogURL: "https://access.redhat.com/errata/RHSA-2026:57365", Digest: "sha256:kept"}}
			client := Client{ErrataURL: server.URL, HTTPClient: server.Client(), ChangelogCache: NewChangelogCache()}
			client.enrichChangelogs(context.Background(), releases)
			if len(releases) != 1 || releases[0].Version != "4.22.11" || releases[0].Digest != "sha256:kept" || releases[0].ChangelogContent != "" {
				t.Fatalf("releases = %#v", releases)
			}
		})
	}
}

func TestChangelogHonorsCallerTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	client := Client{ErrataURL: server.URL, HTTPClient: server.Client(), ChangelogCache: NewChangelogCache()}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := client.changelog(ctx, "RHSA-2026:57365", "https://access.redhat.com/errata/RHSA-2026:57365")
	if err == nil {
		t.Fatal("changelog() succeeded after context timeout")
	}
}

func TestRenderAdvisoryMarkdownShortensAtEntryBoundary(t *testing.T) {
	summary := advisorySummary{
		ID:       "RHBA-2025:23103",
		URL:      "https://access.redhat.com/errata/RHBA-2025:23103",
		Synopsis: "OpenShift Container Platform 4.20.8 bug fix update",
		Type:     "Bug Fix Advisory",
		Issued:   "2025-12-16",
	}
	for i := range 200 {
		summary.Issues = append(summary.Issues, advisoryReference{ID: fmt.Sprintf("OCPBUGS-%05d", i), Title: strings.Repeat("useful title ", 8), URL: fmt.Sprintf("https://issues.redhat.com/browse/OCPBUGS-%05d", i)})
	}
	markdown := renderAdvisoryMarkdown(summary, maxChangelogBytes)
	if len(markdown) > maxChangelogBytes {
		t.Fatalf("markdown length = %d", len(markdown))
	}
	if !strings.Contains(markdown, "Summary shortened") || !strings.Contains(markdown, "full advisory") {
		t.Fatalf("shortening notice missing:\n%s", markdown)
	}
	if strings.Count(markdown, "](") != strings.Count(markdown, ")") {
		t.Fatalf("markdown appears truncated mid-link:\n%s", markdown)
	}
}

func TestTruncateTextPreservesUTF8(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		limit int
		want  string
	}{
		{name: "unchanged", value: "blå", limit: 4, want: "blå"},
		{name: "multibyte boundary", value: "blåbær", limit: 6, want: "blåb…"},
		{name: "one byte", value: "å", limit: 1, want: ""},
		{name: "zero", value: "å", limit: 0, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateText(tc.value, tc.limit)
			if got != tc.want {
				t.Fatalf("truncateText(%q, %d) = %q, want %q", tc.value, tc.limit, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncateText(%q, %d) returned invalid UTF-8 %q", tc.value, tc.limit, got)
			}
			if len(got) > tc.limit {
				t.Fatalf("truncateText(%q, %d) returned %d bytes", tc.value, tc.limit, len(got))
			}
		})
	}
}

func fixtureBody(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func advisoryHTML(id, synopsis string) string {
	return fmt.Sprintf(`<html><body><div>Issued: <time>2025-12-16</time> Updated: <time>2025-12-16</time></div><h1>%s - Bug Fix Advisory</h1><h2>Synopsis</h2><p>%s</p><h2>Type/Severity</h2><p>Bug Fix Advisory</p></body></html>`, id, synopsis)
}
