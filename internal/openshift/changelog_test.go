package openshift

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func TestChangelogURLForArchitecture(t *testing.T) {
	for _, tc := range []struct {
		architecture string
		want         string
		ok           bool
	}{
		{architecture: "amd64", want: "https://amd64.ocp.releases.ci.openshift.org/changelog", ok: true},
		{architecture: "arm64", want: "https://arm64.ocp.releases.ci.openshift.org/changelog", ok: true},
		{architecture: "ppc64le", want: "https://ppc64le.ocp.releases.ci.openshift.org/changelog", ok: true},
		{architecture: "s390x", want: "https://s390x.ocp.releases.ci.openshift.org/changelog", ok: true},
		{architecture: "multi", want: "https://multi.ocp.releases.ci.openshift.org/changelog", ok: true},
		{architecture: "example.com"},
	} {
		got, ok := changelogURLForArchitecture(tc.architecture)
		if got != tc.want || ok != tc.ok {
			t.Errorf("changelogURLForArchitecture(%q) = %q, %v; want %q, %v", tc.architecture, got, ok, tc.want, tc.ok)
		}
	}
}

func TestEnrichChangelogsAddsPerReleaseContentAndCachesIt(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.URL.Query().Get("from"); got != "4.22.11" {
			t.Errorf("from = %q", got)
		}
		if got := r.URL.Query().Get("to"); got != "4.22.12" {
			t.Errorf("to = %q", got)
		}
		_, _ = w.Write([]byte("## Changes from 4.22.11 to 4.22.12\n"))
	}))
	t.Cleanup(server.Close)

	client := Client{ChangelogURL: server.URL, HTTPClient: server.Client(), ChangelogCache: NewChangelogCache()}
	for range 2 {
		releases := []Release{{Version: "4.22.12"}}
		client.enrichChangelogs(context.Background(), "multi", releases)
		if releases[0].ChangelogContent != "## Changes from 4.22.11 to 4.22.12\n" {
			t.Fatalf("changelogContent = %q", releases[0].ChangelogContent)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("changelog requests = %d, want 1", got)
	}
}

func TestChangelogInitializesSuppliedCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("changes"))
	}))
	t.Cleanup(server.Close)

	cache := &ChangelogCache{}
	client := Client{ChangelogURL: server.URL, HTTPClient: server.Client(), ChangelogCache: cache}
	content, err := client.changelog(context.Background(), "multi", "4.22.11", "4.22.12")
	if err != nil {
		t.Fatal(err)
	}
	if content != "changes" {
		t.Fatalf("changelog = %q", content)
	}
	if cache.entries == nil || cache.pending == nil {
		t.Fatal("cache maps were not initialized")
	}
}

func TestChangelogCacheSeparatesEndpoints(t *testing.T) {
	newServer := func(content string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(content))
		}))
	}
	first := newServer("first")
	second := newServer("second")
	t.Cleanup(first.Close)
	t.Cleanup(second.Close)

	cache := NewChangelogCache()
	for _, tc := range []struct {
		server *httptest.Server
		want   string
	}{
		{server: first, want: "first"},
		{server: second, want: "second"},
	} {
		client := Client{ChangelogURL: tc.server.URL, HTTPClient: tc.server.Client(), ChangelogCache: cache}
		got, err := client.changelog(context.Background(), "multi", "4.22.11", "4.22.12")
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Fatalf("changelog = %q, want %q", got, tc.want)
		}
	}
}

func TestEnrichChangelogsCoalescesConcurrentRequests(t *testing.T) {
	var requests atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			close(started)
		}
		<-release
		_, _ = w.Write([]byte("changes"))
	}))
	t.Cleanup(server.Close)

	client := Client{ChangelogURL: server.URL, HTTPClient: server.Client(), ChangelogCache: NewChangelogCache()}
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			releases := []Release{{Version: "4.22.12"}}
			client.enrichChangelogs(context.Background(), "multi", releases)
			if releases[0].ChangelogContent != "changes" {
				t.Errorf("changelogContent = %q", releases[0].ChangelogContent)
			}
		}()
	}
	<-started
	close(release)
	wg.Wait()
	if got := requests.Load(); got != 1 {
		t.Fatalf("changelog requests = %d, want 1", got)
	}
}

func TestEnrichChangelogsFailureDoesNotRemoveRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	releases := []Release{{Version: "4.22.12"}}
	client := Client{ChangelogURL: server.URL, HTTPClient: server.Client(), ChangelogCache: NewChangelogCache()}
	client.enrichChangelogs(context.Background(), "multi", releases)
	if len(releases) != 1 || releases[0].Version != "4.22.12" || releases[0].ChangelogContent != "" {
		t.Fatalf("releases = %#v", releases)
	}
}

func TestPreviousZStream(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    string
		ok      bool
	}{
		{version: "4.22.12", want: "4.22.11", ok: true},
		{version: "4.22.1", want: "4.22.0", ok: true},
		{version: "4.22.0"},
		{version: "invalid"},
	} {
		got, ok := previousZStream(tc.version)
		if got != tc.want || ok != tc.ok {
			t.Errorf("previousZStream(%q) = %q, %v; want %q, %v", tc.version, got, ok, tc.want, tc.ok)
		}
	}
}
