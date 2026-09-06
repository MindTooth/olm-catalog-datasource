package openshift

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestUpdatesReturnsOnlyDirectUnconditionalSuccessorsAndAppliesLag(t *testing.T) {
	const manifestKey = "io.openshift.upgrades.graph.release.manifestref"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("channel"); got != "stable-4.21" {
			t.Errorf("channel = %q", got)
		}
		if got := r.URL.Query().Get("arch"); got != "multi" {
			t.Errorf("arch = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"nodes": []any{
				map[string]any{"version": "4.21.21", "metadata": map[string]string{"url": "https://example.com/21", manifestKey: "quay.io/ocp@sha256:21"}},
				map[string]any{"version": "4.21.23", "metadata": map[string]string{"url": "https://example.com/23", manifestKey: "quay.io/ocp@sha256:23"}},
				map[string]any{"version": "4.21.22", "metadata": map[string]string{"url": "https://example.com/22", manifestKey: "quay.io/ocp@sha256:22"}},
				map[string]any{"version": "4.21.24", "metadata": map[string]string{"url": "https://example.com/24", manifestKey: "quay.io/ocp@sha256:24"}},
			},
			"edges": [][2]int{{0, 1}, {0, 2}, {2, 3}, {0, 1}},
			"conditionalEdges": []any{
				map[string]any{"edges": [][2]int{{0, 3}}},
			},
		})
	}))
	t.Cleanup(server.Close)

	client := Client{GraphURL: server.URL, HTTPClient: server.Client()}
	got, err := client.Updates(context.Background(), UpdateRequest{
		Channel:        "stable-4.21",
		Architecture:   "multi",
		CurrentVersion: "4.21.21",
		Lag:            1,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Release{
		{Version: "4.21.21", ChangelogURL: "https://example.com/21", Digest: "quay.io/ocp@sha256:21"},
		{Version: "4.21.22", ChangelogURL: "https://example.com/22", Digest: "quay.io/ocp@sha256:22"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Updates() = %#v, want %#v", got, want)
	}
}

func TestUpdatesLagCanRemoveEveryTargetButNotCurrent(t *testing.T) {
	server := graphServer(t, map[string]any{
		"nodes": []any{
			map[string]any{"version": "4.21.1"},
			map[string]any{"version": "4.21.2"},
		},
		"edges": [][2]int{{0, 1}},
	})
	client := Client{GraphURL: server.URL, HTTPClient: server.Client()}

	got, err := client.Updates(context.Background(), UpdateRequest{Channel: "stable-4.21", Architecture: "multi", CurrentVersion: "4.21.1", Lag: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Version != "4.21.1" {
		t.Fatalf("Updates() = %#v", got)
	}
}

func TestUpdatesGraphGapAndMinorTransitionKeepOnlyEligibleTargets(t *testing.T) {
	const manifestKey = "io.openshift.upgrades.graph.release.manifestref"
	errata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/")
		_, _ = w.Write([]byte(advisoryHTML(id, "summary for "+id)))
	}))
	t.Cleanup(errata.Close)
	graph := graphServer(t, map[string]any{
		"nodes": []any{
			map[string]any{"version": "4.21.18", "metadata": map[string]string{"url": "https://access.redhat.com/errata/RHBA-2026:100", manifestKey: "sha256:current"}},
			map[string]any{"version": "4.21.19", "metadata": map[string]string{"url": "https://access.redhat.com/errata/RHBA-2026:101", manifestKey: "sha256:intermediate"}},
			map[string]any{"version": "4.21.20", "metadata": map[string]string{"url": "https://access.redhat.com/errata/RHBA-2026:102", manifestKey: "sha256:gap-target"}},
			map[string]any{"version": "4.22.0", "metadata": map[string]string{"url": "https://access.redhat.com/errata/RHEA-2026:103", manifestKey: "sha256:minor-target"}},
		},
		"edges": [][2]int{{0, 2}, {0, 3}},
	})
	client := Client{GraphURL: graph.URL, ErrataURL: errata.URL, HTTPClient: graph.Client(), ChangelogCache: NewChangelogCache()}

	got, err := client.Updates(context.Background(), UpdateRequest{Channel: "stable-4.21", Architecture: "multi", CurrentVersion: "4.21.18"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Version != "4.21.18" || got[1].Version != "4.21.20" || got[2].Version != "4.22.0" {
		t.Fatalf("Updates() = %#v", got)
	}
	if got[0].ChangelogContent != "" {
		t.Fatalf("current release unexpectedly enriched: %#v", got[0])
	}
	for _, release := range got[1:] {
		if release.ChangelogContent == "" || release.Digest == "" || release.ChangelogURL == "" {
			t.Fatalf("target metadata/enrichment missing: %#v", release)
		}
	}
}

func TestUpdatesReportsAbsentCurrentVersion(t *testing.T) {
	server := graphServer(t, map[string]any{"nodes": []any{}, "edges": []any{}})
	client := Client{GraphURL: server.URL, HTTPClient: server.Client()}

	_, err := client.Updates(context.Background(), UpdateRequest{Channel: "stable-4.21", Architecture: "multi", CurrentVersion: "4.21.1"})
	if !errors.Is(err, ErrCurrentVersionNotFound) {
		t.Fatalf("error = %v, want ErrCurrentVersionNotFound", err)
	}
}

func TestUpdatesRejectsInvalidInputAndUpstreamFailure(t *testing.T) {
	client := Client{}
	for _, req := range []UpdateRequest{
		{},
		{Channel: "stable-4.21"},
		{Channel: "stable-4.21", Architecture: "multi"},
		{Channel: "stable-4.21", Architecture: "multi", CurrentVersion: "4.21.1", Lag: -1},
	} {
		if _, err := client.Updates(context.Background(), req); err == nil {
			t.Fatalf("Updates(%#v) succeeded", req)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	client = Client{GraphURL: server.URL, HTTPClient: server.Client()}
	if _, err := client.Updates(context.Background(), UpdateRequest{Channel: "stable-4.21", Architecture: "multi", CurrentVersion: "4.21.1"}); err == nil {
		t.Fatal("Updates() succeeded for an upstream error")
	}
}

func graphServer(t *testing.T, body any) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}
