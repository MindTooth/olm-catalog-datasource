package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestOpenShiftReleasesEndpointPreservesAdvisoryContentAndMetadata(t *testing.T) {
	errata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/errata/RHBA-2025:23103" {
			t.Fatalf("unexpected advisory path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`<html><body>
<div>Issued: <time>2025-12-16</time></div>
<h1>RHBA-2025:23103 - Bug Fix Advisory</h1>
<h2>Synopsis</h2><p>OpenShift Container Platform 4.20.8 bug fix update</p>
<h2>Type/Severity</h2><p>Bug Fix Advisory</p>
<h2>Fixes</h2><ul><li><a href="https://issues.redhat.com/browse/OCPBUGS-61785">OCPBUGS-61785</a> - Console error after upgrading</li></ul>
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
				{"version":"4.20.7","metadata":{"url":"https://access.redhat.com/errata/RHBA-2025:22000","io.openshift.upgrades.graph.release.manifestref":"quay.io/ocp@sha256:current"}},
				{"version":"4.20.8","metadata":{"url":"https://access.redhat.com/errata/RHBA-2025:23103","io.openshift.upgrades.graph.release.manifestref":"quay.io/ocp@sha256:target"}}
			],
			"edges": [[0,1]]
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
	svc.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/openshift-releases/stable-4.20/updates?currentVersion=4.20.7", nil))
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
	if len(body.Releases) != 2 {
		t.Fatalf("releases = %#v", body.Releases)
	}
	target := body.Releases[1]
	if target.Version != "4.20.8" || target.ChangelogURL != "https://access.redhat.com/errata/RHBA-2025:23103" || target.Digest != "quay.io/ocp@sha256:target" {
		t.Fatalf("target metadata = %#v", target)
	}
	if !strings.Contains(target.ChangelogContent, "Release advisory summary") || !strings.Contains(target.ChangelogContent, "OCPBUGS-61785") {
		t.Fatalf("target changelogContent = %q", target.ChangelogContent)
	}
	if body.Releases[0].ChangelogContent != "" {
		t.Fatalf("current release unexpectedly enriched: %#v", body.Releases[0])
	}
}
