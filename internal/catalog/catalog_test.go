package catalog

import (
	"path/filepath"
	"testing"
)

func TestParsePlatform(t *testing.T) {
	tests := []struct {
		name     string
		platform string
		want     [3]string
		wantErr  bool
	}{
		{name: "os and architecture", platform: "linux/amd64", want: [3]string{"linux", "amd64", ""}},
		{name: "with variant", platform: "linux/arm64/v8", want: [3]string{"linux", "arm64", "v8"}},
		{name: "missing os", platform: "/amd64", wantErr: true},
		{name: "missing architecture", platform: "linux/", wantErr: true},
		{name: "too many parts", platform: "linux/arm64/v8/extra", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			osChoice, architectureChoice, variantChoice, err := parsePlatform(tt.platform)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parsePlatform(%q) error = %v, wantErr %t", tt.platform, err, tt.wantErr)
			}
			if got := [3]string{osChoice, architectureChoice, variantChoice}; !tt.wantErr && got != tt.want {
				t.Fatalf("parsePlatform(%q) = %q, want %q", tt.platform, got, tt.want)
			}
		})
	}
}

func TestSafeJoinUsesImageRootForAbsolutePaths(t *testing.T) {
	root := t.TempDir()
	got, err := safeJoin(root, "/configs")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "configs"); got != want {
		t.Fatalf("safeJoin() = %q, want %q", got, want)
	}
	if _, err := safeJoin(root, "/../outside"); err == nil {
		t.Fatal("safeJoin() accepted a path traversal")
	}
}

func TestAddChannelMetaUsesNameField(t *testing.T) {
	snapshot := &Snapshot{Packages: map[string]*Package{}}
	if err := addMeta(snapshot, "olm.channel", []byte(`{"package":"strimzi-kafka-operator","name":"stable","entries":[{"name":"strimzi-cluster-operator.v0.47.0"}]}`)); err != nil {
		t.Fatal(err)
	}
	channel := snapshot.Packages["strimzi-kafka-operator"].Channels["stable"]
	if channel == nil || channel.Name != "stable" {
		t.Fatalf("channel = %#v, want stable", channel)
	}
}
