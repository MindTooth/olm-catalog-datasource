package catalog

import (
	"sort"
	"testing"
)

func TestVersionUpdatesOnlyReturnsDeclaredPath(t *testing.T) {
	p := &Package{Name: "gitops", DefaultChannel: "stable", Bundles: map[string]*Bundle{
		"v1": {Name: "v1", Version: "1.0.0"}, "v2": {Name: "v2", Version: "1.1.0"}, "v3": {Name: "v3", Version: "9.0.0"},
	}, Channels: map[string]*Channel{"stable": {Name: "stable", Entries: []Entry{{Name: "v1"}, {Name: "v2", Replaces: "v1"}, {Name: "v3"}}}}}
	got, err := p.VersionUpdates(UpdateRequest{CurrentVersion: "1.0.0", Mode: "reachable"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "1.0.0" || got[1] != "1.1.0" {
		t.Fatalf("got %#v", got)
	}
}

func TestSkipRange(t *testing.T) {
	p := &Package{Name: "gitops", DefaultChannel: "stable", Bundles: map[string]*Bundle{"v1": {Name: "v1", Version: "1.0.0"}, "v2": {Name: "v2", Version: "2.0.0"}}, Channels: map[string]*Channel{"stable": {Name: "stable", Entries: []Entry{{Name: "v1"}, {Name: "v2", SkipRange: ">=1.0.0 <2.0.0"}}}}}
	got, err := p.VersionUpdates(UpdateRequest{CurrentVersion: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %#v", got)
	}
}

func TestVersionUpdatesReturnsEmptyForVersionOutsideChannel(t *testing.T) {
	p := &Package{Name: "strimzi", Bundles: map[string]*Bundle{
		"v1": {Name: "v1", Version: "1.0.0"},
	}, Channels: map[string]*Channel{
		"stable": {Name: "stable", Entries: []Entry{{Name: "v1"}}},
	}}
	got, err := p.VersionUpdates(UpdateRequest{Channel: "stable", CurrentVersion: "0.47.0", Mode: "reachable"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %#v, want no releases", got)
	}
}

func TestVersionUpdatesRequireCurrentState(t *testing.T) {
	p := &Package{Name: "gitops", DefaultChannel: "stable", Bundles: map[string]*Bundle{
		"v1": {Name: "v1", Version: "1.0.0"},
	}, Channels: map[string]*Channel{
		"stable": {Name: "stable", Entries: []Entry{{Name: "v1"}}},
	}}
	if _, err := p.VersionUpdates(UpdateRequest{}); err == nil {
		t.Fatal("expected current state error")
	}
}

func TestChannelUpdatesRequireCrossChannelEdge(t *testing.T) {
	p := &Package{Name: "gitops", DefaultChannel: "stable-1.20", Bundles: map[string]*Bundle{
		"v120": {Name: "v120", Version: "1.20.4"}, "v121": {Name: "v121", Version: "1.21.0"}, "v999": {Name: "v999", Version: "9.0.0"},
	}, Channels: map[string]*Channel{
		"stable-1.20": {Name: "stable-1.20", Entries: []Entry{{Name: "v120"}}},
		"stable-1.21": {Name: "stable-1.21", Entries: []Entry{{Name: "v121", Replaces: "v120"}}},
		"stable-9":    {Name: "stable-9", Entries: []Entry{{Name: "v999"}}},
	}}
	got, err := p.ChannelUpdates(ChannelUpdateRequest{CurrentChannel: "stable-1.20", CurrentVersion: "1.20.4", Selection: "reachable"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "stable-1.20" || got[1] != "stable-1.21" {
		t.Fatalf("got %#v", got)
	}
}

func TestChannelLessUsesSemanticSuffix(t *testing.T) {
	channels := []string{"gitops-1.10", "gitops-1.6", "gitops-1.21", "latest", "strimzi-0.10.x", "strimzi-0.6.x"}
	sort.Slice(channels, func(i, j int) bool { return ChannelLess(channels[i], channels[j]) })
	want := []string{"gitops-1.6", "gitops-1.10", "gitops-1.21", "latest", "strimzi-0.6.x", "strimzi-0.10.x"}
	for i := range want {
		if channels[i] != want[i] {
			t.Fatalf("channels = %#v, want %#v", channels, want)
		}
	}
}

func TestChannelUpdatesNextPrefersSameVersionedFamily(t *testing.T) {
	p := &Package{Name: "strimzi", Bundles: map[string]*Bundle{
		"v47":    {Name: "v47", Version: "0.47.0"},
		"v48":    {Name: "v48", Version: "0.48.0"},
		"stable": {Name: "stable", Version: "0.51.0"},
	}, Channels: map[string]*Channel{
		"strimzi-0.47.x": {Name: "strimzi-0.47.x", Entries: []Entry{{Name: "v47"}}},
		"strimzi-0.48.x": {Name: "strimzi-0.48.x", Entries: []Entry{{Name: "v48", Replaces: "v47"}}},
		"stable":         {Name: "stable", Entries: []Entry{{Name: "stable", Replaces: "v47"}}},
	}}
	got, err := p.ChannelUpdates(ChannelUpdateRequest{CurrentChannel: "strimzi-0.47.x", CurrentVersion: "0.47.0", Selection: "next"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"strimzi-0.47.x", "strimzi-0.48.x"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestChannelReleasesExposeGraphProvenTargetBundle(t *testing.T) {
	p := &Package{Name: "gitops", Bundles: map[string]*Bundle{
		"v120": {Name: "v120", Version: "1.20.4"},
		"v121": {Name: "v121", Version: "1.21.2"},
	}, Channels: map[string]*Channel{
		"gitops-1.20": {Name: "gitops-1.20", Entries: []Entry{{Name: "v120"}}},
		"gitops-1.21": {Name: "gitops-1.21", Entries: []Entry{{Name: "v121", Replaces: "v120"}}},
	}}
	got, err := p.ChannelReleases(ChannelUpdateRequest{CurrentChannel: "gitops-1.20", CurrentBundle: "v120", Selection: "next"})
	if err != nil {
		t.Fatal(err)
	}
	want := []ChannelRelease{{Channel: "gitops-1.20", Bundle: "v120"}, {Channel: "gitops-1.21", Bundle: "v121"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestChannelUpdatesNextDoesNotMoveToOlderSameFamilyChannel(t *testing.T) {
	p := &Package{Name: "strimzi", Bundles: map[string]*Bundle{
		"v43": {Name: "v43", Version: "0.43.0"},
		"v44": {Name: "v44", Version: "0.44.0"},
		"v46": {Name: "v46", Version: "0.46.0"},
	}, Channels: map[string]*Channel{
		"strimzi-0.43.x": {Name: "strimzi-0.43.x", Entries: []Entry{{Name: "v43"}}},
		"strimzi-0.44.x": {Name: "strimzi-0.44.x", Entries: []Entry{{Name: "v44", Replaces: "v43"}}},
		"strimzi-0.45.x": {Name: "strimzi-0.45.x", Entries: []Entry{{Name: "v43"}}},
		"strimzi-0.46.x": {Name: "strimzi-0.46.x", Entries: []Entry{{Name: "v46", Replaces: "v43"}}},
	}}
	got, err := p.ChannelUpdates(ChannelUpdateRequest{CurrentChannel: "strimzi-0.45.x", CurrentBundle: "v43", Selection: "next"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"strimzi-0.45.x", "strimzi-0.46.x"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
