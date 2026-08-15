package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/MindTooth/olm-catalog-datasource/internal/catalog"
)

func TestResponseRecorderUsesFirstStatusAndCountsBytes(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := &responseRecorder{ResponseWriter: recorder}
	writer.WriteHeader(http.StatusAccepted)
	writer.WriteHeader(http.StatusInternalServerError)
	if _, err := writer.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if got := writer.statusCode(); got != http.StatusAccepted {
		t.Fatalf("statusCode() = %d, want %d", got, http.StatusAccepted)
	}
	if recorder.Code != http.StatusAccepted || writer.bytes != 2 {
		t.Fatalf("code = %d, bytes = %d", recorder.Code, writer.bytes)
	}
}

func TestHealthAndReadinessEndpoints(t *testing.T) {
	svc := New(Config{})

	for _, test := range []struct {
		path string
		want int
	}{
		{path: "/healthz", want: http.StatusOK},
		{path: "/readyz", want: http.StatusServiceUnavailable},
	} {
		res := httptest.NewRecorder()
		svc.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, test.path, nil))
		if res.Code != test.want {
			t.Errorf("GET %s status = %d, want %d", test.path, res.Code, test.want)
		}
	}

	svc.snapshots["catalog"] = &catalog.Snapshot{}
	res := httptest.NewRecorder()
	svc.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if res.Code != http.StatusOK {
		t.Errorf("GET /readyz after a successful refresh status = %d, want %d", res.Code, http.StatusOK)
	}
}

func TestOpenShiftReleasesEndpoint(t *testing.T) {
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("channel") != "stable-4.21" || r.URL.Query().Get("arch") != "multi" {
			t.Errorf("unexpected graph query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{
			"nodes": [
				{"version":"4.21.21","metadata":{"url":"https://example.com/21","io.openshift.upgrades.graph.release.manifestref":"quay.io/ocp@sha256:21"}},
				{"version":"4.21.22","metadata":{"url":"https://example.com/22","io.openshift.upgrades.graph.release.manifestref":"quay.io/ocp@sha256:22"}},
				{"version":"4.21.23","metadata":{"url":"https://example.com/23","io.openshift.upgrades.graph.release.manifestref":"quay.io/ocp@sha256:23"}}
			],
			"edges": [[0,1],[0,2]]
		}`))
	}))
	t.Cleanup(graph.Close)
	svc := New(Config{OpenShiftGraphURL: graph.URL})

	req := httptest.NewRequest(http.MethodGet, "/v1/openshift-releases/stable-4.21/updates?currentVersion=4.21.21&lag=1", nil)
	res := httptest.NewRecorder()
	svc.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var body struct {
		Releases []struct {
			Version      string `json:"version"`
			ChangelogURL string `json:"changelogUrl"`
			Digest       string `json:"digest"`
		} `json:"releases"`
		SourceURL string `json:"sourceUrl"`
		Homepage  string `json:"homepage"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Releases) != 2 || body.Releases[0].Version != "4.21.21" || body.Releases[1].Version != "4.21.22" {
		t.Fatalf("unexpected releases: %#v", body.Releases)
	}
	if body.Releases[1].ChangelogURL != "https://example.com/22" || body.Releases[1].Digest != "quay.io/ocp@sha256:22" {
		t.Fatalf("unexpected target metadata: %#v", body.Releases[1])
	}
	if body.SourceURL != "https://multi.ocp.releases.ci.openshift.org" || body.Homepage != "https://www.openshift.com" {
		t.Fatalf("unexpected source metadata: %#v", body)
	}
}

func TestOpenShiftReleasesEndpointValidation(t *testing.T) {
	svc := New(Config{})
	for _, tc := range []struct {
		method string
		path   string
		want   int
	}{
		{method: http.MethodGet, path: "/v1/openshift-releases/stable-4.21/updates", want: http.StatusBadRequest},
		{method: http.MethodGet, path: "/v1/openshift-releases/stable-4.21/updates?currentVersion=4.21.21&lag=-1", want: http.StatusBadRequest},
		{method: http.MethodGet, path: "/v1/openshift-releases/stable-4.21/updates?currentVersion=4.21.21&lag=tomorrow", want: http.StatusBadRequest},
		{method: http.MethodPost, path: "/v1/openshift-releases/stable-4.21/updates?currentVersion=4.21.21", want: http.StatusMethodNotAllowed},
		{method: http.MethodGet, path: "/v1/openshift-releases/stable-4.21/missing?currentVersion=4.21.21", want: http.StatusNotFound},
	} {
		res := httptest.NewRecorder()
		svc.Handler().ServeHTTP(res, httptest.NewRequest(tc.method, tc.path, nil))
		if res.Code != tc.want {
			t.Errorf("%s %s status = %d, want %d", tc.method, tc.path, res.Code, tc.want)
		}
	}
}

func TestChannelInspectionEndpoint(t *testing.T) {
	svc := New(Config{})
	svc.snapshots["community-v4.20"] = &catalog.Snapshot{Packages: map[string]*catalog.Package{
		"strimzi-kafka-operator": {Name: "strimzi-kafka-operator", DefaultChannel: "stable", Bundles: map[string]*catalog.Bundle{
			"strimzi.v1": {Name: "strimzi.v1", Version: "1.0.0"},
		}, Channels: map[string]*catalog.Channel{
			"stable": {Name: "stable", Entries: []catalog.Entry{{Name: "strimzi.v1"}}},
		}},
	}}
	req := httptest.NewRequest(http.MethodGet, "/v1/catalogs/community-v4.20/packages/strimzi-kafka-operator/channels", nil)
	res := httptest.NewRecorder()
	svc.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var body struct {
		Channels []struct {
			Name  string `json:"name"`
			Heads []struct {
				Bundle  string `json:"bundle"`
				Version string `json:"version"`
			} `json:"heads"`
		} `json:"channels"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Channels) != 1 || body.Channels[0].Name != "stable" || len(body.Channels[0].Heads) != 1 || body.Channels[0].Heads[0].Bundle != "strimzi.v1" {
		t.Fatalf("unexpected response: %#v", body)
	}
}

func TestCatalogStatusEndpoint(t *testing.T) {
	source := catalog.Source{ID: "community-v4.20", Image: "registry.example/catalog:v4.20"}
	svc := New(Config{Sources: []catalog.Source{source}})
	svc.statuses[source.ID] = SourceStatus{Source: source, Available: true, PackageCount: 1}
	req := httptest.NewRequest(http.MethodGet, "/v1/catalogs/community-v4.20/status", nil)
	res := httptest.NewRecorder()
	svc.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
}

func TestRefreshEndpointsQueueConfiguredSources(t *testing.T) {
	source := catalog.Source{ID: "test", Image: ""}
	tokenFile := filepath.Join(t.TempDir(), "refresh-token")
	if err := os.WriteFile(tokenFile, []byte("test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := New(Config{Sources: []catalog.Source{source}, RefreshTokenFile: tokenFile})

	for _, target := range []string{"/v1/refresh", "/v1/catalogs/test/refresh"} {
		req := httptest.NewRequest(http.MethodPost, target, nil)
		req.Header.Set("Authorization", "Bearer test-token")
		res := httptest.NewRecorder()
		svc.Handler().ServeHTTP(res, req)
		if res.Code != http.StatusAccepted {
			t.Fatalf("POST %s status = %d, body = %s", target, res.Code, res.Body.String())
		}
		var body struct {
			Accepted bool `json:"accepted"`
		}
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if !body.Accepted {
			t.Fatalf("POST %s was not accepted", target)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/catalogs/missing/refresh", nil)
	res := httptest.NewRecorder()
	svc.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("missing source status = %d, want %d", res.Code, http.StatusNotFound)
	}
}

func TestRefreshEndpointsRequireConfiguredBearerToken(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "refresh-token")
	if err := os.WriteFile(tokenFile, []byte("test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := catalog.Source{ID: "test"}
	cases := []struct {
		name       string
		tokenFile  string
		authority  string
		wantStatus int
	}{
		{name: "disabled", wantStatus: http.StatusNotFound},
		{name: "unavailable token file", tokenFile: filepath.Join(t.TempDir(), "missing-token"), wantStatus: http.StatusServiceUnavailable},
		{name: "missing token", tokenFile: tokenFile, wantStatus: http.StatusUnauthorized},
		{name: "invalid token", tokenFile: tokenFile, authority: "Bearer wrong", wantStatus: http.StatusUnauthorized},
		{name: "wrong scheme", tokenFile: tokenFile, authority: "Basic test-token", wantStatus: http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := New(Config{Sources: []catalog.Source{source}, RefreshTokenFile: tc.tokenFile})
			for _, target := range []string{"/v1/refresh", "/v1/catalogs/test/refresh"} {
				req := httptest.NewRequest(http.MethodPost, target, nil)
				if tc.authority != "" {
					req.Header.Set("Authorization", tc.authority)
				}
				res := httptest.NewRecorder()
				svc.Handler().ServeHTTP(res, req)
				if res.Code != tc.wantStatus {
					t.Fatalf("POST %s status = %d, want %d; body = %s", target, res.Code, tc.wantStatus, res.Body.String())
				}
			}
		})
	}
}

func TestRefreshTokenCanRotateWithoutReload(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "refresh-token")
	if err := os.WriteFile(tokenFile, []byte("first-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := New(Config{RefreshTokenFile: tokenFile})

	for _, token := range []string{"first-token", "second-token"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/refresh", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		if !svc.authorizeRefresh(res, req) {
			t.Fatalf("token %q was rejected: %d %s", token, res.Code, res.Body.String())
		}
		if token == "first-token" {
			if err := os.WriteFile(tokenFile, []byte("second-token\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/refresh", nil)
	req.Header.Set("Authorization", "Bearer first-token")
	res := httptest.NewRecorder()
	if svc.authorizeRefresh(res, req) || res.Code != http.StatusUnauthorized {
		t.Fatalf("previous token status = %d, want %d", res.Code, http.StatusUnauthorized)
	}
}

func TestReloadDiscardsChangedSourceSnapshot(t *testing.T) {
	old := catalog.Source{ID: "catalog", Image: "old"}
	updated := catalog.Source{ID: "catalog", Image: ""}
	svc := New(Config{Sources: []catalog.Source{old}})
	svc.snapshots[old.ID] = &catalog.Snapshot{Source: old}
	svc.statuses[old.ID] = SourceStatus{Source: old, Available: true}

	svc.Reload(Config{Sources: []catalog.Source{updated}})
	svc.mu.RLock()
	_, exists := svc.snapshots[old.ID]
	svc.mu.RUnlock()
	if exists {
		t.Fatal("changed source snapshot was retained")
	}
}

func TestChannelReleasesEndpointReturnsTargetAndStateToken(t *testing.T) {
	svc := New(Config{})
	svc.snapshots["community-v4.20"] = &catalog.Snapshot{Packages: map[string]*catalog.Package{
		"gitops": {Name: "gitops", Bundles: map[string]*catalog.Bundle{
			"v120": {Name: "v120", Version: "1.20.4"}, "v121": {Name: "v121", Version: "1.21.2"},
		}, Channels: map[string]*catalog.Channel{
			"gitops-1.20": {Name: "gitops-1.20", Entries: []catalog.Entry{{Name: "v120"}}},
			"gitops-1.21": {Name: "gitops-1.21", Entries: []catalog.Entry{{Name: "v121", Replaces: "v120"}}},
		}},
	}}
	req := httptest.NewRequest(http.MethodGet, "/v1/catalogs/community-v4.20/packages/gitops/channel-releases?currentChannel=gitops-1.20&currentBundle=v120&selection=next", nil)
	res := httptest.NewRecorder()
	svc.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var body struct {
		Releases []release `json:"releases"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Releases) != 1 || body.Releases[0].Version != "gitops-1.21" || body.Releases[0].Digest != "v121" {
		t.Fatalf("unexpected response: %#v", body)
	}
}
