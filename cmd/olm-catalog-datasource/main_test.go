package main

import "testing"

func TestParseConfig(t *testing.T) {
	config, err := parseConfig([]byte(`
debug: true
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
}
