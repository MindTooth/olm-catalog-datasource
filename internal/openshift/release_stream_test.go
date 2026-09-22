package openshift

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestUpdatesKeepsAllStreamReleasesWithErrataOnlyChangelogs(t *testing.T) {
	const advisoryPrefix = "https://access.redhat.com/errata/RHBA-2026:1"
	changelogRequests := 0
	client := Client{
		GraphURL:             "https://graph.example.test",
		ErrataURL:            "https://errata.example.test",
		ReleaseControllerURL: "https://release-controller.example.test",
		HTTPClient: &http.Client{Transport: contractRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			var body string
			switch req.URL.Hostname() {
			case "graph.example.test":
				body = `{"nodes":[
					{"version":"4.22.9"},
					{"version":"4.22.10","metadata":{"url":"` + advisoryPrefix + `10"}},
					{"version":"4.22.11","metadata":{"url":"` + advisoryPrefix + `11"}},
					{"version":"4.22.12","metadata":{"url":"` + advisoryPrefix + `12"}},
					{"version":"4.22.13","metadata":{"url":"` + advisoryPrefix + `13"}}
				],"edges":[[0,2],[0,4]]}`
			case "release-controller.example.test":
				if req.URL.Path != "/api/v1/releasestream/4-stable-multi/tags" {
					changelogRequests++
					return nil, fmt.Errorf("unexpected release-controller changelog request: %s", req.URL)
				}
				body = `{"tags":[
					{"name":"4.22.9","phase":"Accepted"},
					{"name":"4.22.10","phase":"Accepted"},
					{"name":"4.22.11","phase":"Accepted"},
					{"name":"4.22.12","phase":"Accepted"},
					{"name":"4.22.13","phase":"Accepted"}]}`
			case "errata.example.test":
				id := strings.TrimPrefix(req.URL.Path, "/")
				body = advisoryHTML(id, "summary for "+id)
			default:
				return nil, fmt.Errorf("unexpected request: %s", req.URL)
			}
			return testHTTPResponse(req, http.StatusOK, body), nil
		})},
		ChangelogCache: NewChangelogCache(),
	}

	releases, err := client.Updates(context.Background(), UpdateRequest{Channel: "eus-4.22", Architecture: "multi", CurrentVersion: "4.22.9"})
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 5 {
		t.Fatalf("releases = %#v", releases)
	}
	for i, patch := range []string{"9", "10", "11", "12", "13"} {
		release := releases[i]
		if release.Version != "4.22."+patch {
			t.Fatalf("release[%d] = %#v", i, release)
		}
		if i == 0 {
			if release.ChangelogContent != "" {
				t.Fatalf("installed release has changelog content: %#v", release)
			}
			continue
		}
		id := "RHBA-2026:1" + patch
		if release.ChangelogURL != advisoryPrefix+patch || !strings.Contains(release.ChangelogContent, "summary for "+id) || strings.Contains(release.ChangelogContent, "release-controller") {
			t.Fatalf("release %s does not use its linked advisory: %#v", release.Version, release)
		}
		if release.IsDeprecated != (patch == "10" || patch == "12") {
			t.Fatalf("release %s eligibility changed: %#v", release.Version, release)
		}
	}
	if changelogRequests != 0 {
		t.Fatalf("release-controller changelog requests = %d", changelogRequests)
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
