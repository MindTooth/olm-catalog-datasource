package catalog_test

import (
	"context"
	"io/fs"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/MindTooth/openshift-versioning/pkg/catalog"
)

// Exercise the public loading and query API without a registry, server, or cluster.
func TestReadFSAndResolve(t *testing.T) {
	configs := fstest.MapFS{
		"operator/catalog.json": {Data: []byte(`
{"schema":"olm.channel","package":"example","name":"stable","entries":[{"name":"example.v1.1.0","replaces":"example.v1.0.0"},{"name":"example.v1.0.0"}]}
{"schema":"olm.bundle","package":"example","name":"example.v1.1.0","image":"registry.example/operator:1.1.0","properties":[{"type":"olm.package","value":{"packageName":"example","version":"1.1.0"}}]}
{"schema":"olm.package","name":"example","defaultChannel":"stable"}
{"schema":"olm.bundle","package":"example","name":"example.v1.0.0","properties":[{"type":"olm.package","value":{"packageName":"example","version":"1.0.0"}}]}
{"schema":"olm.channel","package":"example","name":"next","entries":[{"name":"example.v2.0.0","replaces":"example.v1.1.0"}]}
{"schema":"olm.bundle","package":"example","name":"example.v2.0.0","properties":[{"type":"olm.package","value":{"packageName":"example","version":"2.0.0"}}]}
`)},
	}
	source := catalog.Source{ID: "local"}
	snapshot, err := (catalog.Reader{}).ReadFS(context.Background(), source, configs)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Source != source || snapshot.GeneratedAt.IsZero() || len(snapshot.Packages) != 1 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	p := snapshot.Packages["example"]
	if p == nil || p.Name != "example" || p.DefaultChannel != "stable" {
		t.Fatalf("unexpected package: %#v", p)
	}
	if got := p.Channels["stable"].Entries[0].Name; got != "example.v1.0.0" {
		t.Fatalf("first entry = %q, want normalized name order", got)
	}
	versions, err := p.VersionUpdates(catalog.UpdateRequest{CurrentVersion: "1.0.0", Mode: "reachable"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"1.0.0", "1.1.0"}; !reflect.DeepEqual(versions, want) {
		t.Fatalf("versions = %v, want %v", versions, want)
	}
	heads := p.ChannelHeads("stable")
	if len(heads) != 1 || heads[0].Version != "1.1.0" || heads[0].Image != "registry.example/operator:1.1.0" {
		t.Fatalf("unexpected heads: %#v", heads)
	}
	releases, err := p.ChannelReleases(catalog.ChannelUpdateRequest{CurrentChannel: "stable", CurrentVersion: "1.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	want := []catalog.ChannelRelease{{Channel: "stable", Bundle: "example.v1.1.0"}, {Channel: "next", Bundle: "example.v2.0.0"}}
	if !reflect.DeepEqual(releases, want) {
		t.Fatalf("channel releases = %v, want %v", releases, want)
	}
}

func TestReadFSErrors(t *testing.T) {
	for name, configs := range map[string]fs.FS{
		"malformed JSON":   fstest.MapFS{"catalog.json": {Data: []byte(`{`)}},
		"invalid metadata": fstest.MapFS{"catalog.json": {Data: []byte(`{"schema":"olm.package"}`)}},
	} {
		t.Run(name, func(t *testing.T) {
			snapshot, err := (catalog.Reader{}).ReadFS(context.Background(), catalog.Source{}, configs)
			if err == nil || snapshot != nil {
				t.Fatalf("ReadFS() = (%#v, %v), want nil snapshot and error", snapshot, err)
			}
		})
	}
}
