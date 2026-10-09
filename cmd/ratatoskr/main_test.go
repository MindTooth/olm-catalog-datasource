package main

import (
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAndLoadWithDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("channels: [v4.22]\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(path); err != nil {
		t.Fatalf("load() error = %v", err)
	}
	if _, digest, err := loadWithDigest(path); err != nil || digest != sha256.Sum256(data) {
		t.Fatalf("loadWithDigest() digest = %x, error = %v", digest, err)
	}
	if _, err := load(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("load() accepted a missing file")
	}
	if _, _, err := loadWithDigest(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("loadWithDigest() accepted a missing file")
	}
}

func TestMainVersionCommand(t *testing.T) {
	oldArgs, oldStdout := os.Args, os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"ratatoskr", "version"}
	os.Stdout = w
	t.Cleanup(func() { os.Args, os.Stdout = oldArgs, oldStdout })
	main()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "ratatoskr dev\n" {
		t.Fatalf("version output = %q", output)
	}
}

func TestWriteReleases(t *testing.T) {
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = oldStdout })
	writeReleases([]string{"1.2.3", "2.0.0"})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(output); !strings.Contains(got, `"version":"1.2.3"`) || !strings.Contains(got, `"version":"2.0.0"`) {
		t.Fatalf("release output = %s", got)
	}
}
