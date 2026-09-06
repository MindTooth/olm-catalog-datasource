package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestOpenShiftReleasesEndpointKeepsPerReleaseAdvisories(t *testing.T) {
	errata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/errata/")
		if id == "RHBA-2026:112" {
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`<html><body>
<div>Issued: <time>2026-08-01</time></div>
<h1>` + id + ` - Bug Fix Advisory</h1>
<h2>Synopsis</h2><p>summary for ` + id + `</p>
<h2>Type/Severity</h2><p>Bug Fix Advisory</p>
</body></html>`))
	}))
	t.Cleanup(errata.Close)
	errataURL, err := url.Parse(errata.URL)
	if err != nil {
		t.Fatal(err)
	}

	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"nodes": [
				{"version":"4.22.9","metadata":{"url":"https://access.redhat.com/errata/RHBA-2026:109","io.openshift.upgrades.graph.release.manifestref":"quay.io/ocp@sha256:109"}},
				{"version":"4.22.10","metadata":{"url":"https://access.redhat.com/errata/RHBA-2026:110","io.openshift.upgrades.graph.release.manifestref":"quay.io/ocp@sha256:110"}},
				{"version":"4.22.11","metadata":{"url":"https://access.redhat.com/errata/RHBA-2026:111","io.openshift.upgrades.graph.release.manifestref":"quay.io/ocp@sha256:111"}},
				{"version":"4.22.12","metadata":{"url":"https://access.redhat.com/errata/RHBA-2026:112","io.openshift.upgrades.graph.release.manifestref":"quay.io/ocp@sha256:112"}},
				{"version":"4.22.13","metadata":{"url":"https://access.redhat.com/errata/RHBA-2026:113","io.openshift.upgrades.graph.release.manifestref":"quay.io/ocp@sha256:113"}}
			],
			"edges": [[0,2],[0,3],[0,4]],
			"conditionalEdges": [{"edges":[[0,1]]}]
		}`))
	}))
	t.Cleanup(graph.Close)

	transport := http.DefaultTransport
	svc := New(Config{OpenShiftGraphURL: graph.URL})
	svc.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() == "access.redhat.com" {
			clone := req.Clone(req.Context())
			clone.URL.Scheme = errataURL.Scheme
			clone.URL.Host = errataURL.Host
			return transport.RoundTrip(clone)
		}
		return transport.RoundTrip(req)
	})}

	res := httptest.NewRecorder()
	svc.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/openshift-releases/stable-4.22/updates?currentVersion=4.22.9", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}

	var body struct {
		Releases []struct {
			Version          string `json:"version"`
			ChangelogContent string `json:"changelogContent"`
			ChangelogURL     string `json:"changelogUrl"`
			Digest           string `json:"digest"`
		} `json:"releases"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}

	wantVersions := []string{"4.22.9", "4.22.11", "4.22.12", "4.22.13"}
	if len(body.Releases) != len(wantVersions) {
		t.Fatalf("releases = %#v", body.Releases)
	}
	total := 0
	for i, release := range body.Releases {
		if release.Version != wantVersions[i] {
			t.Fatalf("release[%d].Version = %q, want %q", i, release.Version, wantVersions[i])
		}
		total += len(release.ChangelogContent)
	}
	if total > 24_000 {
		t.Fatalf("aggregate changelogContent = %d bytes", total)
	}
	if body.Releases[0].ChangelogContent != "" {
		t.Fatalf("installed release unexpectedly enriched: %#v", body.Releases[0])
	}

	for _, index := range []int{1, 3} {
		release := body.Releases[index]
		id := "RHBA-2026:1" + strings.TrimPrefix(release.Version, "4.22.")
		if !strings.Contains(release.ChangelogContent, id) || strings.Count(release.ChangelogContent, "### Release advisory summary") != 1 {
			t.Fatalf("%s changelogContent = %q", release.Version, release.ChangelogContent)
		}
		for _, other := range []string{"RHBA-2026:111", "RHBA-2026:113"} {
			if other != id && strings.Contains(release.ChangelogContent, other) {
				t.Fatalf("%s includes another release advisory %s", release.Version, other)
			}
		}
	}

	missing := body.Releases[2]
	if missing.Version != "4.22.12" || missing.ChangelogContent != "" {
		t.Fatalf("missing advisory changed release: %#v", missing)
	}

	for _, release := range body.Releases[1:] {
		patch := strings.TrimPrefix(release.Version, "4.22.")
		wantURL := "https://access.redhat.com/errata/RHBA-2026:1" + patch
		wantDigest := "quay.io/ocp@sha256:1" + patch
		if release.ChangelogURL != wantURL || release.Digest != wantDigest {
			t.Fatalf("metadata changed for %s: %#v", release.Version, release)
		}
	}
	for _, release := range body.Releases {
		if release.Version == "4.22.10" {
			t.Fatal("conditional graph-gap release became an update candidate")
		}
	}
}
