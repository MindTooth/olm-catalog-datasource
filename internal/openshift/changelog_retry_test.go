package openshift

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestChangelogColdFailureIsRetriedOnLaterLookup(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(advisoryHTML("RHBA-2025:23103", "available on retry")))
	}))
	t.Cleanup(server.Close)

	client := Client{ErrataURL: server.URL, HTTPClient: server.Client(), ChangelogCache: NewChangelogCache()}
	const id = "RHBA-2025:23103"
	const advisoryURL = "https://access.redhat.com/errata/RHBA-2025:23103"
	if _, err := client.changelog(context.Background(), id, advisoryURL); err == nil {
		t.Fatal("first changelog lookup unexpectedly succeeded")
	}
	content, err := client.changelog(context.Background(), id, advisoryURL)
	if err != nil || !strings.Contains(content, "available on retry") {
		t.Fatalf("retry content = %q, err = %v", content, err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
}

func TestSameAdvisoryIdentitySharesCacheAcrossReleaseVersions(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(advisoryHTML("RHBA-2025:23103", "shared advisory")))
	}))
	t.Cleanup(server.Close)

	client := Client{ErrataURL: server.URL, HTTPClient: server.Client(), ChangelogCache: NewChangelogCache()}
	releases := []Release{
		{Version: "4.20.8", ChangelogURL: "https://access.redhat.com/errata/RHBA-2025:23103"},
		{Version: "4.20.9", ChangelogURL: "https://access.redhat.com/errata/RHBA-2025:23103"},
	}
	client.enrichChangelogs(context.Background(), releases)
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
	if releases[0].ChangelogContent == "" || releases[0].ChangelogContent != releases[1].ChangelogContent {
		t.Fatalf("release content = %#v", releases)
	}
}
