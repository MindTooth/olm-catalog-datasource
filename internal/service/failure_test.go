package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/MindTooth/ratatoskr/pkg/catalog"
)

func TestOpenShiftDatasourceFailures(t *testing.T) {
	const validGraph = `{"nodes":[{"version":"4.22.9"},{"version":"4.22.11"}],"edges":[[0,1]]}`
	for _, tc := range []struct {
		name, body string
		status     int
		err        error
		stream     bool
		timeout    bool
		readError  bool
	}{
		{name: "graph HTTP error", status: http.StatusBadGateway},
		{name: "graph transport error", err: io.ErrUnexpectedEOF},
		{name: "graph timeout", timeout: true},
		{name: "malformed graph", body: `{`},
		{name: "missing graph data", body: `{}`},
		{name: "null graph", body: `null`},
		{name: "missing edges", body: `{"nodes":[{"version":"4.22.9"}]}`},
		{name: "invalid edge before absent current", body: `{"nodes":[],"edges":[[0,1]]}`},
		{name: "incomplete edge", body: `{"nodes":[{"version":"4.22.9"}],"edges":[[0]]}`},
		{name: "missing node version", body: `{"nodes":[{}],"edges":[]}`},
		{name: "trailing graph data", body: validGraph + `{}`},
		{name: "graph read failure after JSON", body: validGraph, readError: true},
		{name: "null edge index", body: `{"nodes":[{"version":"4.22.9"},{"version":"4.22.11"}],"edges":[[null,1]]}`},
		{name: "duplicate node version", body: `{"nodes":[{"version":"4.22.9"},{"version":"4.22.9"}],"edges":[]}`},
		{name: "partial discovery HTTP error", stream: true, status: http.StatusServiceUnavailable},
		{name: "partial discovery transport error", stream: true, err: io.ErrUnexpectedEOF},
		{name: "partial discovery timeout", stream: true, timeout: true},
		{name: "partial discovery malformed JSON", stream: true, body: `{`},
		{name: "partial discovery missing tags", stream: true, body: `{}`},
		{name: "partial discovery null tags", stream: true, body: `{"tags":null}`},
		{name: "partial discovery incomplete tag", stream: true, body: `{"tags":[{"name":"4.22.10"}]}`},
		{name: "partial discovery read failure after JSON", stream: true, body: `{"tags":[]}`, readError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := New(Config{OpenShiftTimeout: 20 * time.Millisecond})
			streamRequested := false
			svc.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				isStream := req.URL.Hostname() != "api.openshift.com"
				streamRequested = streamRequested || isStream
				status, body := http.StatusOK, validGraph
				if tc.stream == isStream {
					if tc.timeout {
						<-req.Context().Done()
						return nil, req.Context().Err()
					}
					if tc.err != nil {
						return nil, tc.err
					}
					body = tc.body
					if tc.status != 0 {
						status = tc.status
					}
				}
				var reader io.Reader = strings.NewReader(body)
				if tc.readError && tc.stream == isStream {
					reader = io.MultiReader(reader, iotest.ErrReader(io.ErrUnexpectedEOF))
				}
				return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(reader), Header: make(http.Header)}, nil
			})}
			res := httptest.NewRecorder()
			svc.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/openshift-releases/stable-4.22/updates?currentVersion=4.22.9", nil))
			if res.Code != http.StatusServiceUnavailable || strings.Contains(res.Body.String(), `"releases"`) {
				t.Fatalf("failed lookup returned status %d, body %s", res.Code, res.Body.String())
			}
			if tc.stream && !streamRequested {
				t.Fatal("partial discovery test did not reach release stream lookup")
			}
		})
	}
}

func TestOpenShiftDatasourceLegitimateEmpty(t *testing.T) {
	for _, body := range []string{
		`{"nodes":[],"edges":[]}`,
		`{"nodes":[{"version":"4.22.10"}],"edges":[]}`,
	} {
		svc := New(Config{OpenShiftGraphURL: "https://graph.example.test"})
		svc.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})}
		res := httptest.NewRecorder()
		svc.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/openshift-releases/stable-4.22/updates?currentVersion=4.22.9", nil))
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"releases":[]`) {
			t.Fatalf("valid empty lookup returned status %d, body %s", res.Code, res.Body.String())
		}
	}
}

func TestCatalogDatasourceUnavailable(t *testing.T) {
	source := catalog.Source{ID: "community-v4.22", Image: "registry.example/catalog:v4.22"}
	for _, failure := range []bool{false, true} {
		svc := New(Config{Sources: []catalog.Source{source}})
		if failure {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := svc.Refresh(ctx, source); !errors.Is(err, context.Canceled) {
				t.Fatalf("Refresh() = %v, want context.Canceled", err)
			}
		}
		for _, route := range []string{
			"/v1/catalogs/community-v4.22/packages/example/updates?currentVersion=1.0.0",
			"/v1/catalogs/community-v4.22/packages/example/channel-updates?currentChannel=stable&currentVersion=1.0.0",
			"/v1/catalogs/community-v4.22/packages/example/channel-releases?currentChannel=stable&currentVersion=1.0.0",
			"/v2/catalogs/community/4.22/packages/example/updates?currentVersion=1.0.0",
			"/v2/catalogs/community/4.22/packages/example/channel-updates?currentChannel=stable&currentVersion=1.0.0",
			"/v2/sources/community-v4.22/packages/example/updates?currentVersion=1.0.0",
			"/v2/sources/community-v4.22/packages/example/channel-updates?currentChannel=stable&currentVersion=1.0.0",
		} {
			res := httptest.NewRecorder()
			svc.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, route, nil))
			if res.Code != http.StatusServiceUnavailable || strings.Contains(res.Body.String(), `"releases"`) {
				t.Fatalf("GET %s returned status %d, body %s", route, res.Code, res.Body.String())
			}
		}
	}
}

func TestCatalogDatasourceLegitimateEmpty(t *testing.T) {
	svc := New(Config{})
	svc.snapshots["community-v4.22"] = &catalog.Snapshot{GeneratedAt: time.Now().UTC(), Packages: map[string]*catalog.Package{
		"example": {Name: "example", DefaultChannel: "stable", Channels: map[string]*catalog.Channel{"stable": {Name: "stable"}}, Bundles: map[string]*catalog.Bundle{}},
	}}
	for _, route := range []string{
		"/v1/catalogs/community-v4.22/packages/example/updates?currentVersion=1.0.0",
		"/v1/catalogs/community-v4.22/packages/example/channel-updates?currentChannel=stable&currentVersion=1.0.0",
		"/v1/catalogs/community-v4.22/packages/example/channel-releases?currentChannel=stable&currentVersion=1.0.0",
		"/v2/catalogs/community/4.22/packages/example/updates?currentVersion=1.0.0",
		"/v2/catalogs/community/4.22/packages/example/channel-updates?currentChannel=stable&currentVersion=1.0.0",
		"/v2/sources/community-v4.22/packages/example/updates?currentVersion=1.0.0",
		"/v2/sources/community-v4.22/packages/example/channel-updates?currentChannel=stable&currentVersion=1.0.0",
	} {
		res := httptest.NewRecorder()
		svc.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, route, nil))
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"releases":[]`) {
			t.Fatalf("GET %s returned status %d, body %s", route, res.Code, res.Body.String())
		}
	}
}
