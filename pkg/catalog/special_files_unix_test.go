//go:build unix

package catalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReadRejectsFIFO(t *testing.T) {
	for _, name := range []string{"catalog.json", ".indexignore", "linked.json"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			configs := filepath.Join(root, "configs")
			if err := os.Mkdir(configs, 0o700); err != nil {
				t.Fatal(err)
			}
			fifo := filepath.Join(configs, name)
			if name == "linked.json" {
				fifo = filepath.Join(root, "fifo")
			}
			if err := unix.Mkfifo(fifo, 0o600); err != nil {
				t.Fatal(err)
			}
			if name == "linked.json" {
				if err := os.Symlink("../fifo", filepath.Join(configs, name)); err != nil {
					t.Fatal(err)
				}
			}
			for _, extracted := range []bool{false, true} {
				t.Run(map[bool]string{false: "ReadFS", true: "readExtracted"}[extracted], func(t *testing.T) {
					done := make(chan error, 1)
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
					defer cancel()
					go func() {
						var snapshot *Snapshot
						var err error
						if extracted {
							snapshot, err = (Reader{}).readExtracted(ctx, Source{}, root, "/configs")
						} else {
							snapshot, err = (Reader{}).ReadFS(ctx, Source{}, os.DirFS(configs))
						}
						if snapshot != nil {
							done <- nil
							return
						}
						done <- err
					}()
					select {
					case err := <-done:
						if err == nil || !strings.Contains(err.Error(), "catalog file is not regular") {
							t.Fatalf("want special-file rejection, got %v", err)
						}
					case <-time.After(time.Second):
						// Release a regressed blocking open without ever blocking the cleanup itself.
						fd, err := unix.Open(fifo, unix.O_WRONLY|unix.O_NONBLOCK, 0)
						if err == nil {
							_ = unix.Close(fd)
						}
						select {
						case <-done:
						case <-time.After(time.Second):
						}
						t.Fatal("catalog FIFO read blocked without a writer")
					}
				})
			}
		})
	}
}
