package openshift

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type contractRoundTripFunc func(*http.Request) (*http.Response, error)

func (f contractRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestUpdatesKeepsChangelogContentPerRelease(t *testing.T) {
	const manifestKey = "io.openshift.upgrades.graph.release.manifestref"
	graphBody, err := json.Marshal(map[string]any{
		"nodes": []any{
			map[string]any{"version": "4.22.9", "metadata": map[string]string{"url": "https://access.redhat.com/errata/RHBA-2026:109", manifestKey: "sha256:109"}},
			map[string]any{"version": "4.22.10", "metadata": map[string]string{"url": "https://access.redhat.com/errata/RHBA-2026:110", manifestKey: "sha256:110"}},
			map[string]any{"version": "4.22.11", "metadata": map[string]string{"url": "https://access.redhat.com/errata/RHBA-2026:111", manifestKey: "sha256:111"}},
			map[string]any{"version": "4.22.12", "metadata": map[string]string{"url": "https://access.redhat.com/errata/RHBA-2026:112", manifestKey: "sha256:112"}},
			map[string]any{"version": "4.22.13", "metadata": map[string]string{"url": "https://access.redhat.com/errata/RHBA-2026:113", manifestKey: "sha256:113"}},
		},
		"edges": [][2]int{{0, 2}, {0, 3}, {0, 4}},
	})
	if err != nil {
		t.Fatal(err)
	}

	streamBody := `<html><body><table>
<tr><td><a href="/releasetag/4.22.10">4.22.10</a></td><td>Accepted</td></tr>
<tr><td><a href="/releasetag/4.22.11">4.22.11</a></td><td>Accepted</td></tr>
<tr><td><a href="/releasetag/4.22.12">4.22.12</a></td><td>Accepted</td></tr>
<tr><td><a href="/releasetag/4.22.13">4.22.13</a></td><td>Accepted</td></tr>
</table></body></html>`

	var releaseStreamRequests atomic.Int32
	client := Client{
		GraphURL: "https://graph.example.test",
		HTTPClient: &http.Client{Transport: contractRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			var body string
			switch req.URL.Hostname() {
			case "graph.example.test":
				if req.URL.Query().Get("channel") != "stable-4.22" || req.URL.Query().Get("arch") != "multi" {
					t.Fatalf("graph query = %s", req.URL.RawQuery)
				}
				body = string(graphBody)
			case "access.redhat.com":
				id := strings.TrimPrefix(req.URL.Path, "/errata/")
				body = advisoryHTML(id, "summary for "+id)
			case "multi.ocp.releases.ci.openshift.org":
				releaseStreamRequests.Add(1)
				body = streamBody
			default:
				return nil, fmt.Errorf("unexpected request to %s", req.URL)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    req,
			}, nil
		})},
		ChangelogCache: NewChangelogCache(),
	}

	releases, err := client.Updates(context.Background(), UpdateRequest{
		Channel:        "stable-4.22",
		Architecture:   "multi",
		CurrentVersion: "4.22.9",
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := releaseStreamRequests.Load(); got != 0 {
		t.Fatalf("release-controller requests = %d, want 0", got)
	}
	wantVersions := []string{"4.22.9", "4.22.11", "4.22.12", "4.22.13"}
	if len(releases) != len(wantVersions) {
		t.Fatalf("releases = %#v", releases)
	}
	for i, want := range wantVersions {
		if releases[i].Version != want {
			t.Fatalf("release[%d].Version = %q, want %q", i, releases[i].Version, want)
		}
	}
	if releases[0].ChangelogContent != "" {
		t.Fatalf("installed release unexpectedly enriched: %#v", releases[0])
	}

	for i, release := range releases[1:] {
		id := fmt.Sprintf("RHBA-2026:%d", 111+i)
		if !strings.Contains(release.ChangelogContent, id) || strings.Count(release.ChangelogContent, "### Release advisory summary") != 1 {
			t.Fatalf("%s content is not exactly one advisory summary:\n%s", release.Version, release.ChangelogContent)
		}
		for other := 110; other <= 113; other++ {
			otherID := fmt.Sprintf("RHBA-2026:%d", other)
			if otherID != id && strings.Contains(release.ChangelogContent, otherID) {
				t.Fatalf("%s content contains advisory for another release %s:\n%s", release.Version, otherID, release.ChangelogContent)
			}
		}
		if release.Digest != fmt.Sprintf("sha256:%d", 111+i) {
			t.Fatalf("%s digest = %q", release.Version, release.Digest)
		}
		if release.ChangelogURL != "https://access.redhat.com/errata/"+id {
			t.Fatalf("%s changelogUrl = %q", release.Version, release.ChangelogURL)
		}
	}
	for _, release := range releases {
		if release.Version == "4.22.10" {
			t.Fatal("graph-gap release 4.22.10 became an update candidate")
		}
	}
}
