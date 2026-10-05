package cluster

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestResolve(t *testing.T) {
	sub := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "gitops", "namespace": "openshift-gitops-operator"},
		"spec": map[string]any{
			"name": "openshift-gitops-operator", "channel": "gitops-1.20",
			"source": "redhat-operators", "sourceNamespace": "openshift-marketplace",
		},
	}}
	manifest := unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"packageName": "openshift-gitops-operator", "catalogSource": "redhat-operators",
			"catalogSourceNamespace": "openshift-marketplace", "defaultChannel": "gitops-1.21",
			"channels": []any{
				map[string]any{"name": "gitops-1.20", "currentCSVDesc": map[string]any{"version": "1.20.8"}},
				map[string]any{"name": "gitops-1.21", "currentCSVDesc": map[string]any{"version": "1.21.2"}},
			},
		},
	}}

	got, ok := Resolve(sub, []unstructured.Unstructured{manifest})
	if !ok {
		t.Fatal("expected subscription to resolve")
	}
	if got.CurrentChannelVersion != "1.20.8" {
		t.Fatalf("current channel version = %q, want 1.20.8", got.CurrentChannelVersion)
	}
	if got.DefaultChannel != "gitops-1.21" || got.DefaultChannelVersion != "1.21.2" {
		t.Fatalf("default = %s/%s, want gitops-1.21/1.21.2", got.DefaultChannel, got.DefaultChannelVersion)
	}
}

func TestResolveRejectsDifferentCatalog(t *testing.T) {
	sub := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"name": "example", "channel": "stable", "source": "redhat-operators", "sourceNamespace": "olm"},
	}}
	manifest := unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"packageName": "example", "catalogSource": "community-operators", "catalogSourceNamespace": "olm",
			"defaultChannel": "stable",
			"channels":       []any{map[string]any{"name": "stable", "currentCSVDesc": map[string]any{"version": "1.2.3"}}},
		},
	}}
	if _, ok := Resolve(sub, []unstructured.Unstructured{manifest}); ok {
		t.Fatal("expected different catalog source not to resolve")
	}
}

func TestResolveChannelDefaults(t *testing.T) {
	for _, tc := range []struct {
		name           string
		channel        string
		defaultChannel string
		channels       []string
		wantOK         bool
	}{
		{name: "omitted subscription channel", defaultChannel: "stable", channels: []string{"stable", "beta"}, wantOK: true},
		{name: "implicit package default", channel: "stable", channels: []string{"stable"}, wantOK: true},
		{name: "both defaults implicit", channels: []string{"stable"}, wantOK: true},
		{name: "ambiguous package default", channel: "stable", channels: []string{"stable", "beta"}},
		{name: "no channels"},
		{name: "unknown subscription channel", channel: "missing", defaultChannel: "stable", channels: []string{"stable"}},
		{name: "unknown default channel", channel: "stable", defaultChannel: "missing", channels: []string{"stable"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := map[string]any{"name": "example", "source": "catalog", "sourceNamespace": "olm"}
			if tc.channel != "" {
				spec["channel"] = tc.channel
			}
			sub := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
			channels := make([]any, 0, len(tc.channels))
			for _, name := range tc.channels {
				channels = append(channels, map[string]any{"name": name, "currentCSVDesc": map[string]any{"version": "1.2.3"}})
			}
			manifest := unstructured.Unstructured{Object: map[string]any{
				"status": map[string]any{
					"packageName": "example", "catalogSource": "catalog", "catalogSourceNamespace": "olm",
					"defaultChannel": tc.defaultChannel, "channels": channels,
				},
			}}
			got, ok := Resolve(sub, []unstructured.Unstructured{manifest})
			if ok != tc.wantOK {
				t.Fatalf("resolved = %t, want %t", ok, tc.wantOK)
			}
			if ok && (got.CurrentChannel != "stable" || got.DefaultChannel != "stable" || got.CurrentChannelVersion != "1.2.3" || got.DefaultChannelVersion != "1.2.3") {
				t.Fatalf("incorrect effective channels: %+v", got)
			}
		})
	}
}

func TestResolveCatalogNamespace(t *testing.T) {
	for _, sourceNamespace := range []string{"", "olm", "missing"} {
		t.Run("sourceNamespace="+sourceNamespace, func(t *testing.T) {
			sub := &unstructured.Unstructured{Object: map[string]any{
				"spec": map[string]any{"name": "example", "channel": "stable", "source": "catalog", "sourceNamespace": sourceNamespace},
			}}
			var manifests []unstructured.Unstructured
			for i, namespace := range []string{"other", "olm"} {
				version := []string{"9.9.9", "1.2.3"}[i]
				manifests = append(manifests, unstructured.Unstructured{Object: map[string]any{
					"status": map[string]any{
						"packageName": "example", "catalogSource": "catalog", "catalogSourceNamespace": namespace,
						"defaultChannel": "stable",
						"channels":       []any{map[string]any{"name": "stable", "currentCSVDesc": map[string]any{"version": version}}},
					},
				}})
			}
			got, ok := Resolve(sub, manifests)
			if wantOK := sourceNamespace == "olm"; ok != wantOK {
				t.Fatalf("resolved = %t, want %t", ok, wantOK)
			}
			if ok && (got.CurrentChannelVersion != "1.2.3" || got.DefaultChannelVersion != "1.2.3") {
				t.Fatalf("resolved versions from wrong catalog namespace: %+v", got)
			}
		})
	}
}
