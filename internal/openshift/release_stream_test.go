package openshift

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestUpdatesAddsReleaseStreamChangelogsWithoutAddingUpdateCandidates(t *testing.T) {
	const manifestKey = "io.openshift.upgrades.graph.release.manifestref"
	var mu sync.Mutex
	changelogRequests := map[string]int{}
	client := Client{
		GraphURL:             "https://graph.example.test",
		ErrataURL:            "https://errata.example.test",
		ReleaseControllerURL: "https://release-controller.example.test",
		HTTPClient: &http.Client{Transport: contractRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			var body string
			switch req.URL.Hostname() {
			case "graph.example.test":
				body = `{"nodes":[
{"version":"4.22.9","metadata":{"url":"https://access.redhat.com/errata/RHBA-2026:109","` + manifestKey + `":"sha256:109"}},
{"version":"4.22.11","metadata":{"url":"https://access.redhat.com/errata/RHBA-2026:111","` + manifestKey + `":"sha256:111"}},
{"version":"4.22.13","metadata":{"url":"https://access.redhat.com/errata/RHBA-2026:113","` + manifestKey + `":"sha256:113"}}
],"edges":[[0,1],[0,2]]}`
			case "errata.example.test":
				body = advisoryHTML(strings.TrimPrefix(req.URL.Path, "/"), "graph advisory")
			case "release-controller.example.test":
				switch req.URL.Path {
				case "/api/v1/releasestream/4-stable-multi/tags":
					body = `{"tags":[
{"name":"4.22.13","phase":"Accepted"},{"name":"4.22.12","phase":"Accepted"},
{"name":"4.22.11","phase":"Accepted"},{"name":"4.22.10","phase":"Accepted"},
{"name":"4.22.9","phase":"Accepted"},{"name":"4.23.0-ec.1","phase":"Accepted"}]}`
				case "/changelog":
					key := req.URL.Query().Get("from") + "-" + req.URL.Query().Get("to")
					mu.Lock()
					changelogRequests[key]++
					mu.Unlock()
					body = fmt.Sprintf("## %s\n\nRelease-controller changes for %s.", key, key)
				default:
					return nil, fmt.Errorf("unexpected release-controller request: %s", req.URL)
				}
			default:
				return nil, fmt.Errorf("unexpected request: %s", req.URL)
			}
			return testHTTPResponse(req, http.StatusOK, body), nil
		})},
		ChangelogCache: NewChangelogCache(),
	}
	request := UpdateRequest{Channel: "stable-4.22", Architecture: "multi", CurrentVersion: "4.22.9"}
	got, err := client.Updates(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("releases = %#v", got)
	}
	wantVersions := []string{"4.22.9", "4.22.10", "4.22.11", "4.22.12", "4.22.13"}
	for i, want := range wantVersions {
		if got[i].Version != want {
			t.Fatalf("release[%d].Version = %q, want %q", i, got[i].Version, want)
		}
	}
	for _, version := range []string{"4.22.10", "4.22.12"} {
		release := findRelease(t, got, version)
		if !release.IsDeprecated {
			t.Fatalf("stream-only release %s is an update candidate: %#v", version, release)
		}
		if !strings.Contains(release.ChangelogContent, "Release-controller changes") || !strings.Contains(release.ChangelogURL, "/changelog?") {
			t.Fatalf("stream-only release %s lacks controller changelog metadata: %#v", version, release)
		}
	}
	for _, version := range []string{"4.22.11", "4.22.13"} {
		release := findRelease(t, got, version)
		if release.IsDeprecated {
			t.Fatalf("graph release %s became deprecated: %#v", version, release)
		}
		if !strings.Contains(release.ChangelogContent, "Release-controller changes") {
			t.Fatalf("graph release %s lacks release-controller content: %#v", version, release)
		}
		if release.ChangelogURL != "https://access.redhat.com/errata/RHBA-2026:1"+strings.TrimPrefix(version, "4.22.") {
			t.Fatalf("graph release %s lost graph metadata: %#v", version, release)
		}
	}

	if _, err := client.Updates(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, key := range []string{"4.22.9-4.22.10", "4.22.10-4.22.11", "4.22.11-4.22.12", "4.22.12-4.22.13"} {
		if changelogRequests[key] != 1 {
			t.Fatalf("changelog %s requested %d times, want 1", key, changelogRequests[key])
		}
	}
}

func TestReleaseStreamChangelogFailureDoesNotSuppressRelease(t *testing.T) {
	var failedRequests atomic.Int32
	client := Client{
		GraphURL:             "https://graph.example.test",
		ReleaseControllerURL: "https://release-controller.example.test",
		HTTPClient: &http.Client{Transport: contractRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Hostname() {
			case "graph.example.test":
				return testHTTPResponse(req, http.StatusOK, `{"nodes":[{"version":"4.22.9"},{"version":"4.22.11"}],"edges":[[0,1]]}`), nil
			case "release-controller.example.test":
				switch req.URL.Path {
				case "/api/v1/releasestream/4-stable-multi/tags":
					return testHTTPResponse(req, http.StatusOK, `{"tags":[{"name":"4.22.11","phase":"Accepted"},{"name":"4.22.10","phase":"Accepted"},{"name":"4.22.9","phase":"Accepted"}]}`), nil
				case "/changelog":
					if req.URL.Query().Get("to") == "4.22.10" {
						if failedRequests.Add(1) == 1 {
							return testHTTPResponse(req, http.StatusBadGateway, "unavailable"), nil
						}
					}
					return testHTTPResponse(req, http.StatusOK, "## Available changelog"), nil
				}
			}
			return nil, fmt.Errorf("unexpected request: %s", req.URL)
		})},
		ChangelogCache: NewChangelogCache(),
	}
	got, err := client.Updates(context.Background(), UpdateRequest{Channel: "stable-4.22", Architecture: "multi", CurrentVersion: "4.22.9"})
	if err != nil {
		t.Fatal(err)
	}
	missing := findRelease(t, got, "4.22.10")
	if !missing.IsDeprecated || missing.ChangelogContent != "" {
		t.Fatalf("failed stream changelog changed release semantics: %#v", missing)
	}
	if target := findRelease(t, got, "4.22.11"); target.IsDeprecated || target.ChangelogContent == "" {
		t.Fatalf("successful graph target changed after stream failure: %#v", target)
	}
	got, err = client.Updates(context.Background(), UpdateRequest{Channel: "stable-4.22", Architecture: "multi", CurrentVersion: "4.22.9"})
	if err != nil {
		t.Fatal(err)
	}
	if retry := findRelease(t, got, "4.22.10"); retry.ChangelogContent == "" {
		t.Fatalf("cold failure was cached instead of retried: %#v", retry)
	}
	if failedRequests.Load() != 2 {
		t.Fatalf("failed changelog requests = %d, want 2", failedRequests.Load())
	}
}

func testHTTPResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func findRelease(t *testing.T, releases []Release, version string) Release {
	t.Helper()
	for _, release := range releases {
		if release.Version == version {
			return release
		}
	}
	t.Fatalf("release %s not found in %#v", version, releases)
	return Release{}
}
