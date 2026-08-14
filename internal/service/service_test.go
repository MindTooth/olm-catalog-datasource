package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	svc := New(Config{Sources: []catalog.Source{source}})

	for _, target := range []string{"/v1/refresh", "/v1/catalogs/test/refresh"} {
		req := httptest.NewRequest(http.MethodPost, target, nil)
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
