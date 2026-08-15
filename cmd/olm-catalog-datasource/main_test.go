package main

import (
	"testing"
	"time"
)

func TestParseConfig(t *testing.T) {
	config, err := parseConfig([]byte(`
debug: true
openshiftGraphURL: https://graph.example.test/api
openshiftTimeout: 45s
sources:
  - id: community
    image: registry.example.com/catalog:latest
`))
	if err != nil {
		t.Fatal(err)
	}
	if !config.Debug || len(config.Sources) != 1 || config.Sources[0].ID != "community" {
		t.Fatalf("got %#v", config)
	}
	serviceConfig, err := toService(config)
	if err != nil {
		t.Fatal(err)
	}
	if serviceConfig.OpenShiftGraphURL != "https://graph.example.test/api" || serviceConfig.OpenShiftTimeout != 45*time.Second {
		t.Fatalf("unexpected OpenShift config: %#v", serviceConfig)
	}
}
