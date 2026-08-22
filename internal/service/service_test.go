package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	if body.SourceURL != "https://multi.ocp.releases.ci.openshift.org" || body.Homepage != "https://openshift.com" {
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
	source := catalog.Source{ID: "test-v4.20", Image: ""}
	tokenFile := filepath.Join(t.TempDir(), "refresh-token")
	if err := os.WriteFile(tokenFile, []byte("test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := New(Config{Sources: []catalog.Source{source}, RefreshTokenFile: tokenFile})

	for _, target := range []string{
		"/v1/refresh",
		"/v1/catalogs/test-v4.20/refresh",
		"/v2/catalogs/refresh",
		"/v2/catalogs/test/4.20/refresh",
		"/v2/sources/test-v4.20/refresh",
	} {
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

	req := httptest.NewRequest(http.MethodPost, "/v2/catalogs/missing/4.20/refresh", nil)
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
	source := catalog.Source{ID: "test-v4.20"}
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
			for _, target := range []string{
				"/v1/refresh",
				"/v1/catalogs/test-v4.20/refresh",
				"/v2/catalogs/refresh",
				"/v2/catalogs/test/4.20/refresh",
				"/v2/sources/test-v4.20/refresh",
			} {
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

func TestV2CatalogAndSourceRoutes(t *testing.T) {
	generated := catalog.Source{ID: "community-v4.20", Image: "registry.example/community:v4.20"}
	private := catalog.Source{ID: "private", Image: "registry.example/private:latest"}
	svc := New(Config{Sources: []catalog.Source{generated, private}})
	packageData := &catalog.Package{Name: "gitops", DefaultChannel: "stable", Bundles: map[string]*catalog.Bundle{
		"v1": {Name: "v1", Version: "1.0.0"},
		"v2": {Name: "v2", Version: "1.1.0"},
	}, Channels: map[string]*catalog.Channel{
		"stable":   {Name: "stable", Entries: []catalog.Entry{{Name: "v1"}, {Name: "v2", Replaces: "v1"}}},
		"stable-2": {Name: "stable-2", Entries: []catalog.Entry{{Name: "v2", Replaces: "v1"}}},
	}}
	svc.snapshots[generated.ID] = &catalog.Snapshot{Source: generated, Packages: map[string]*catalog.Package{"gitops": packageData}}
	svc.snapshots[private.ID] = &catalog.Snapshot{Source: private, Packages: map[string]*catalog.Package{"gitops": packageData}}
	svc.statuses[generated.ID] = SourceStatus{Source: generated, Available: true, PackageCount: 1}
	svc.statuses[private.ID] = SourceStatus{Source: private, Available: true, PackageCount: 1}

	for _, target := range []string{
		"/v2/catalogs/community/4.20",
		"/v2/catalogs/community/v4.20",
		"/v2/sources/private",
		"/v2/sources/private/packages/gitops",
	} {
		res := httptest.NewRecorder()
		svc.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, target, nil))
		if res.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, body = %s", target, res.Code, res.Body.String())
		}
	}

	res := httptest.NewRecorder()
	svc.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v2/catalogs", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("GET /v2/catalogs status = %d, body = %s", res.Code, res.Body.String())
	}
	var catalogs struct {
		Catalogs []struct {
			Catalog string `json:"catalog"`
			Version string `json:"version"`
		} `json:"catalogs"`
	}
	if err := json.NewDecoder(res.Body).Decode(&catalogs); err != nil {
		t.Fatal(err)
	}
	if len(catalogs.Catalogs) != 1 || catalogs.Catalogs[0].Catalog != "community" || catalogs.Catalogs[0].Version != "4.20" {
		t.Fatalf("unexpected catalog list: %#v", catalogs)
	}

	res = httptest.NewRecorder()
	svc.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v2/catalogs/community/4.20/packages/gitops?include=channels,bundles,graph", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("package details status = %d, body = %s", res.Code, res.Body.String())
	}
	var details struct {
		ChannelCount int `json:"channelCount"`
		BundleCount  int `json:"bundleCount"`
		Channels     []struct {
			Name string `json:"name"`
		} `json:"channels"`
		Bundles []struct {
			Name string `json:"name"`
		} `json:"bundles"`
		Graph []struct {
			Name string `json:"name"`
		} `json:"graph"`
	}
	if err := json.NewDecoder(res.Body).Decode(&details); err != nil {
		t.Fatal(err)
	}
	if details.ChannelCount != 2 || details.BundleCount != 2 || len(details.Channels) != 2 || len(details.Bundles) != 2 || len(details.Graph) != 2 {
		t.Fatalf("unexpected package details: %#v", details)
	}

	res = httptest.NewRecorder()
	svc.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v2/catalogs/community/4.20/packages/gitops/updates?operatorChannel=stable&currentBundle=v1", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("version updates status = %d, body = %s", res.Code, res.Body.String())
	}
	var updates struct {
		Releases []release `json:"releases"`
	}
	if err := json.NewDecoder(res.Body).Decode(&updates); err != nil {
		t.Fatal(err)
	}
	if len(updates.Releases) != 2 || updates.Releases[1].Version != "1.1.0" {
		t.Fatalf("unexpected version updates: %#v", updates)
	}

	res = httptest.NewRecorder()
	svc.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v2/catalogs/community/4.20/packages/gitops/channel-updates?currentChannel=stable&currentBundle=v1&selection=next", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("channel updates status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := json.NewDecoder(res.Body).Decode(&updates); err != nil {
		t.Fatal(err)
	}
	if len(updates.Releases) != 1 || updates.Releases[0].Version != "stable-2" || updates.Releases[0].Digest != "v2" {
		t.Fatalf("unexpected channel updates: %#v", updates)
	}

	res = httptest.NewRecorder()
	svc.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v2/catalogs/community/4.20/packages/gitops?include=metadata", nil))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("invalid package include status = %d, want %d", res.Code, http.StatusBadRequest)
	}
}

func TestV2SourceRoutesSupportEscapedSourceID(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "refresh-token")
	if err := os.WriteFile(tokenFile, []byte("test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := catalog.Source{ID: "team/private", Image: ""}
	svc := New(Config{Sources: []catalog.Source{source}, RefreshTokenFile: tokenFile})
	svc.snapshots[source.ID] = &catalog.Snapshot{Source: source, Packages: map[string]*catalog.Package{
		"gitops": {Name: "gitops", DefaultChannel: "stable", Bundles: map[string]*catalog.Bundle{}, Channels: map[string]*catalog.Channel{}},
	}}

	for _, tc := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/v2/sources/team%2Fprivate"},
		{method: http.MethodGet, path: "/v2/sources/team%2Fprivate/packages"},
		{method: http.MethodGet, path: "/v2/sources/team%2Fprivate/packages/gitops"},
		{method: http.MethodPost, path: "/v2/sources/team%2Fprivate/refresh"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.method == http.MethodPost {
				req.Header.Set("Authorization", "Bearer test-token")
			}
			res := httptest.NewRecorder()
			svc.Handler().ServeHTTP(res, req)
			want := http.StatusOK
			if tc.method == http.MethodPost {
				want = http.StatusAccepted
			}
			if res.Code != want {
				t.Fatalf("status = %d, want %d; body = %s", res.Code, want, res.Body.String())
			}
		})
	}
}

func TestCatalogRouteContracts(t *testing.T) {
	source := catalog.Source{ID: "community-v4.20", Image: "registry.example/community:v4.20"}
	packageData := &catalog.Package{Name: "gitops", DefaultChannel: "stable", Bundles: map[string]*catalog.Bundle{
		"v1": {Name: "v1", Version: "1.0.0", Image: "registry.example/gitops:v1"},
		"v2": {Name: "v2", Version: "1.1.0", Deprecated: true},
	}, Channels: map[string]*catalog.Channel{
		"stable":   {Name: "stable", Entries: []catalog.Entry{{Name: "v1"}, {Name: "v2", Replaces: "v1"}}},
		"stable-2": {Name: "stable-2", Entries: []catalog.Entry{{Name: "v2", Replaces: "v1"}}},
	}}
	svc := New(Config{Sources: []catalog.Source{source}})
	svc.snapshots[source.ID] = &catalog.Snapshot{Source: source, Packages: map[string]*catalog.Package{"gitops": packageData}}
	svc.statuses[source.ID] = SourceStatus{Source: source, Available: true, PackageCount: 1}

	for _, tc := range []struct {
		name, method, path string
		want               int
		allow              string
		contains           string
	}{
		{name: "list v1 catalogs", method: http.MethodGet, path: "/v1/catalogs", want: http.StatusOK, contains: `"catalogs"`},
		{name: "list packages with paging", method: http.MethodGet, path: "/v1/catalogs/community-v4.20/packages?prefix=git&limit=1", want: http.StatusOK, contains: `"limit":1`},
		{name: "version updates", method: http.MethodGet, path: "/v1/catalogs/community-v4.20/packages/gitops/updates?channel=stable&currentBundle=v1", want: http.StatusOK, contains: `"version":"1.1.0"`},
		{name: "channel updates", method: http.MethodGet, path: "/v1/catalogs/community-v4.20/packages/gitops/channel-updates?currentChannel=stable&currentBundle=v1&selection=next", want: http.StatusOK, contains: `"version":"stable-2"`},
		{name: "bundles filtered by channel", method: http.MethodGet, path: "/v1/catalogs/community-v4.20/packages/gitops/bundles?channel=stable", want: http.StatusOK, contains: `"image":"registry.example/gitops:v1"`},
		{name: "unknown bundle channel", method: http.MethodGet, path: "/v1/catalogs/community-v4.20/packages/gitops/bundles?channel=missing", want: http.StatusNotFound},
		{name: "graph filtered by channel", method: http.MethodGet, path: "/v1/catalogs/community-v4.20/packages/gitops/graph?channel=stable", want: http.StatusOK, contains: `"stable"`},
		{name: "unknown graph channel", method: http.MethodGet, path: "/v1/catalogs/community-v4.20/packages/gitops/graph?channel=missing", want: http.StatusNotFound},
		{name: "invalid resolve reports reason", method: http.MethodGet, path: "/v1/catalogs/community-v4.20/packages/gitops/resolve?channel=stable", want: http.StatusOK, contains: `"valid":false`},
		{name: "unavailable catalog", method: http.MethodGet, path: "/v1/catalogs/missing/packages", want: http.StatusServiceUnavailable},
		{name: "missing package", method: http.MethodGet, path: "/v1/catalogs/community-v4.20/packages/missing/bundles", want: http.StatusNotFound},
		{name: "v1 wrong method", method: http.MethodPost, path: "/v1/catalogs", want: http.StatusMethodNotAllowed, allow: http.MethodGet},
		{name: "list v2 sources", method: http.MethodGet, path: "/v2/sources", want: http.StatusOK, contains: `"sources"`},
		{name: "invalid v2 catalog version", method: http.MethodGet, path: "/v2/catalogs/community/4.20.1", want: http.StatusNotFound},
		{name: "v2 wrong method", method: http.MethodPost, path: "/v2/catalogs", want: http.StatusMethodNotAllowed, allow: http.MethodGet},
		{name: "unknown v2 package action", method: http.MethodGet, path: "/v2/catalogs/community/4.20/packages/gitops/bundles", want: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := httptest.NewRecorder()
			svc.Handler().ServeHTTP(res, httptest.NewRequest(tc.method, tc.path, nil))
			if res.Code != tc.want {
				t.Fatalf("%s %s status = %d, want %d; body = %s", tc.method, tc.path, res.Code, tc.want, res.Body.String())
			}
			if tc.allow != "" && res.Header().Get("Allow") != tc.allow {
				t.Errorf("Allow = %q, want %q", res.Header().Get("Allow"), tc.allow)
			}
			if tc.contains != "" && !strings.Contains(res.Body.String(), tc.contains) {
				t.Errorf("body = %s, want substring %q", res.Body.String(), tc.contains)
			}
		})
	}
}

func TestRefreshRouteErrorContracts(t *testing.T) {
	source := catalog.Source{ID: "community-v4.20"}
	emptyToken := filepath.Join(t.TempDir(), "empty-token")
	if err := os.WriteFile(emptyToken, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, method, path string
		tokenFile          string
		want               int
		allow              string
	}{
		{name: "v1 all refresh requires post", method: http.MethodGet, path: "/v1/refresh", want: http.StatusMethodNotAllowed, allow: http.MethodPost},
		{name: "v1 source refresh requires post", method: http.MethodGet, path: "/v1/catalogs/community-v4.20/refresh", want: http.StatusMethodNotAllowed, allow: http.MethodPost},
		{name: "v2 all refresh requires post", method: http.MethodGet, path: "/v2/catalogs/refresh", want: http.StatusMethodNotAllowed, allow: http.MethodPost},
		{name: "v2 source refresh requires post", method: http.MethodGet, path: "/v2/catalogs/community/4.20/refresh", want: http.StatusMethodNotAllowed, allow: http.MethodPost},
		{name: "empty token is unavailable", method: http.MethodPost, path: "/v2/catalogs/community/4.20/refresh", tokenFile: emptyToken, want: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := New(Config{Sources: []catalog.Source{source}, RefreshTokenFile: tc.tokenFile})
			res := httptest.NewRecorder()
			svc.Handler().ServeHTTP(res, httptest.NewRequest(tc.method, tc.path, nil))
			if res.Code != tc.want {
				t.Fatalf("%s %s status = %d, want %d; body = %s", tc.method, tc.path, res.Code, tc.want, res.Body.String())
			}
			if tc.allow != "" && res.Header().Get("Allow") != tc.allow {
				t.Errorf("Allow = %q, want %q", res.Header().Get("Allow"), tc.allow)
			}
		})
	}
}
