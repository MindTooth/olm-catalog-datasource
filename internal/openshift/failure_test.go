package openshift

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestUpdatesRejectsPartialDiscovery(t *testing.T) {
	for _, failure := range []error{io.ErrUnexpectedEOF, context.DeadlineExceeded} {
		client := Client{
			GraphURL:             "https://graph.example.test",
			ReleaseControllerURL: "https://stream.example.test",
			HTTPClient: &http.Client{Transport: contractRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Hostname() == "stream.example.test" {
					return nil, failure
				}
				return testHTTPResponse(req, http.StatusOK, `{"nodes":[{"version":"4.22.9"},{"version":"4.22.11"}],"edges":[[0,1]]}`), nil
			})},
		}
		releases, err := client.Updates(context.Background(), UpdateRequest{Channel: "stable-4.22", Architecture: "multi", CurrentVersion: "4.22.9"})
		if !errors.Is(err, failure) || releases != nil {
			t.Fatalf("Updates() = (%#v, %v), want nil releases and %v", releases, err, failure)
		}
	}
}

func TestUpdatesAcceptsEmptyDiscovery(t *testing.T) {
	client := Client{
		GraphURL:             "https://graph.example.test",
		ReleaseControllerURL: "https://stream.example.test",
		HTTPClient: &http.Client{Transport: contractRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := `{"nodes":[{"version":"4.22.9"},{"version":"4.22.11"}],"edges":[[0,1]]}`
			if req.URL.Hostname() == "stream.example.test" {
				body = `{"tags":[]}`
			}
			return testHTTPResponse(req, http.StatusOK, body), nil
		})},
	}
	releases, err := client.Updates(context.Background(), UpdateRequest{Channel: "stable-4.22", Architecture: "multi", CurrentVersion: "4.22.9"})
	if err != nil || len(releases) != 2 || releases[0].Version != "4.22.9" || releases[1].Version != "4.22.11" {
		t.Fatalf("Updates() = (%#v, %v), want both graph releases", releases, err)
	}
}

func TestDecodeReleaseDataRejectsIncompleteBodies(t *testing.T) {
	for name, body := range map[string]io.Reader{
		"trailing data":           strings.NewReader(`{"tags":[]} {}`),
		"too large":               strings.NewReader(`{"tags":[],"padding":"` + strings.Repeat("x", 100) + `"}`),
		"read failure after JSON": io.MultiReader(strings.NewReader(`{"tags":[]}`), failingReader{}),
	} {
		t.Run(name, func(t *testing.T) {
			var payload releaseControllerTags
			if err := decodeReleaseData(body, 64, &payload); err == nil {
				t.Fatal("incomplete response accepted")
			}
		})
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
