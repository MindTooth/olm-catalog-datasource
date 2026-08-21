package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MindTooth/olm-catalog-datasource/internal/catalog"
)

func TestExampleConfig(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test file")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(data); err != nil {
		t.Fatalf("config.example.yaml: %v", err)
	}
}

func TestParseExpandsChannelWithDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`channels: ["4.22"]`))
	if err != nil {
		t.Fatal(err)
	}
	want := []catalog.Source{
		{ID: "redhat-v4.22", Image: "registry.redhat.io/redhat/redhat-operator-index:v4.22", Platform: DefaultPlatform},
		{ID: "certified-v4.22", Image: "registry.redhat.io/redhat/certified-operator-index:v4.22", Platform: DefaultPlatform},
		{ID: "community-v4.22", Image: "registry.redhat.io/redhat/community-operator-index:v4.22", Platform: DefaultPlatform},
	}
	if len(cfg.Service.Sources) != len(want) {
		t.Fatalf("sources = %#v, want %#v", cfg.Service.Sources, want)
	}
	for i := range want {
		if cfg.Service.Sources[i] != want[i] {
			t.Errorf("sources[%d] = %#v, want %#v", i, cfg.Service.Sources[i], want[i])
		}
	}
	if cfg.Service.RefreshInterval != DefaultRefreshInterval || cfg.Service.RefreshTimeout != DefaultRefreshTimeout || cfg.Service.OpenShiftTimeout != DefaultOpenShiftTimeout || cfg.Service.ParseConcurrency != DefaultParseConcurrency {
		t.Fatalf("unexpected defaults: %#v", cfg.Service)
	}
}

func TestParseSelectsCatalogsAndCanonicalizesChannels(t *testing.T) {
	cfg, err := Parse([]byte(`
platform: linux/arm64
channels: [v4.22, "4.23"]
catalogs: [community, redhat]
`))
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{"community-v4.22", "redhat-v4.22", "community-v4.23", "redhat-v4.23"}
	if len(cfg.Service.Sources) != len(wantIDs) {
		t.Fatalf("sources = %#v", cfg.Service.Sources)
	}
	for i, id := range wantIDs {
		if source := cfg.Service.Sources[i]; source.ID != id || source.Platform != "linux/arm64" {
			t.Errorf("sources[%d] = %#v, want id %q on linux/arm64", i, source, id)
		}
	}
}

func TestExplicitSourcesReplaceOrExtendGeneratedSources(t *testing.T) {
	cfg, err := Parse([]byte(`
channels: ["4.22"]
sources:
  - id: community-v4.22
    image: mirror.example/community:v4.22
    platform: linux/arm64
  - id: private
    image: registry.example/private:latest
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Service.Sources) != 4 {
		t.Fatalf("sources = %#v", cfg.Service.Sources)
	}
	override := cfg.Service.Sources[2]
	if override.Image != "mirror.example/community:v4.22" || override.Platform != "linux/arm64" {
		t.Fatalf("override = %#v", override)
	}
	custom := cfg.Service.Sources[3]
	if custom.ID != "private" || custom.Platform != DefaultPlatform {
		t.Fatalf("custom source = %#v", custom)
	}
}

func TestLegacySourcesRemainValidAndInheritGlobalPlatform(t *testing.T) {
	cfg, err := Parse([]byte(`
platform: linux/arm64/v8
refreshInterval: 1h
debug: true
openshiftGraphURL: https://graph.example.test/api
openshiftTimeout: 45s
sources:
  - id: legacy
    image: registry.example/catalog:latest
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Service.Sources) != 1 || cfg.Service.Sources[0].Platform != "linux/arm64/v8" {
		t.Fatalf("sources = %#v", cfg.Service.Sources)
	}
	if cfg.Service.RefreshInterval != time.Hour {
		t.Fatalf("refresh interval = %s", cfg.Service.RefreshInterval)
	}
	if !cfg.Debug || cfg.Service.OpenShiftGraphURL != "https://graph.example.test/api" || cfg.Service.OpenShiftTimeout != 45*time.Second {
		t.Fatalf("runtime settings = %#v", cfg)
	}
}

func TestParseRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		message string
	}{
		{name: "empty", yaml: `{}`, message: "at least one channel or source"},
		{name: "unknown field", yaml: "channels: [v4.22]\nchanels: [v4.23]", message: "field chanels not found"},
		{name: "unquoted channel", yaml: `channels: [4.22]`, message: "quote versions"},
		{name: "patch channel", yaml: `channels: [v4.22.1]`, message: "expected major.minor"},
		{name: "duplicate normalized channel", yaml: `channels: [v4.22, "4.22"]`, message: `duplicate catalog channel "v4.22"`},
		{name: "catalogs without channel", yaml: "catalogs: [redhat]\nsources:\n  - id: x\n    image: example/x", message: "catalogs requires"},
		{name: "duplicate catalog", yaml: "channels: [v4.22]\ncatalogs: [redhat, redhat]", message: `duplicate catalog "redhat"`},
		{name: "unknown catalog", yaml: "channels: [v4.22]\ncatalogs: [marketplace]", message: `unknown catalog "marketplace"`},
		{name: "invalid global platform", yaml: "platform: amd64\nchannels: [v4.22]", message: "expected os/architecture"},
		{name: "invalid source platform", yaml: "sources:\n  - id: x\n    image: example/x\n    platform: linux", message: `source "x" platform`},
		{name: "missing source image", yaml: "sources:\n  - id: x", message: "requires id and image"},
		{name: "duplicate explicit source", yaml: "sources:\n  - {id: x, image: example/x}\n  - {id: x, image: example/y}", message: `duplicate explicit source id "x"`},
		{name: "negative concurrency", yaml: "parseConcurrency: -1\nchannels: [v4.22]", message: "at least 1"},
		{name: "bad duration", yaml: "refreshTimeout: later\nchannels: [v4.22]", message: "refreshTimeout"},
		{name: "multiple documents", yaml: "channels: [v4.22]\n---\nchannels: [v4.23]", message: "multiple YAML documents"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.yaml))
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error = %v, want message containing %q", err, test.message)
			}
		})
	}
}
