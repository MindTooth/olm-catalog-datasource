// Package catalog reads file-based operator catalogs without invoking opm.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/operator-framework/operator-registry/alpha/declcfg"
	"github.com/operator-framework/operator-registry/pkg/containertools"
	"github.com/operator-framework/operator-registry/pkg/image"
	"github.com/operator-framework/operator-registry/pkg/image/containersimageregistry"
	"go.podman.io/image/v5/types"
)

// Source identifies a catalog image. ID is part of the public HTTP API.
type Source struct {
	ID       string `yaml:"id" json:"id"`
	Image    string `yaml:"image" json:"image"`
	Platform string `yaml:"platform,omitempty" json:"platform,omitempty"`
}

// Snapshot is intentionally compact: it contains graph metadata, not rendered CSVs.
type Snapshot struct {
	Source      Source              `json:"source"`
	GeneratedAt time.Time           `json:"generatedAt"`
	Packages    map[string]*Package `json:"packages"`
}

type Package struct {
	Name           string              `json:"name"`
	DefaultChannel string              `json:"defaultChannel"`
	Channels       map[string]*Channel `json:"channels"`
	Bundles        map[string]*Bundle  `json:"bundles"`
}

type Channel struct {
	Name       string  `json:"name"`
	Entries    []Entry `json:"entries"`
	Deprecated bool    `json:"deprecated"`
}

type Entry struct {
	Name      string   `json:"name"`
	Replaces  string   `json:"replaces,omitempty"`
	Skips     []string `json:"skips,omitempty"`
	SkipRange string   `json:"skipRange,omitempty"`
}

type Bundle struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Image      string `json:"image,omitempty"`
	Deprecated bool   `json:"deprecated"`
}

type Reader struct {
	SignaturePolicy  string
	ParseConcurrency int
}

// Read pulls, unpacks, and streams FBC metadata. It deliberately avoids action.Render,
// which constructs a complete DeclarativeConfig including large bundle objects.
func (r Reader) Read(ctx context.Context, source Source) (*Snapshot, error) {
	if source.ID == "" || source.Image == "" {
		return nil, fmt.Errorf("catalog source requires id and image")
	}
	sys := &types.SystemContext{}
	if r.SignaturePolicy != "" {
		sys.SignaturePolicyPath = r.SignaturePolicy
	}
	if source.Platform != "" {
		osChoice, architectureChoice, variantChoice, err := parsePlatform(source.Platform)
		if err != nil {
			return nil, err
		}
		sys.OSChoice = osChoice
		sys.ArchitectureChoice = architectureChoice
		sys.VariantChoice = variantChoice
	}
	registry, err := containersimageregistry.New(sys)
	if err != nil {
		return nil, fmt.Errorf("create image registry: %w", err)
	}
	defer registry.Destroy()

	ref := image.SimpleReference(source.Image)
	if err := registry.Pull(ctx, ref); err != nil {
		return nil, fmt.Errorf("pull %q: %w", source.Image, err)
	}
	labels, err := registry.Labels(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("labels %q: %w", source.Image, err)
	}
	configs, ok := labels[containertools.ConfigsLocationLabel]
	if !ok {
		return nil, fmt.Errorf("%q is not a file-based catalog image", source.Image)
	}
	root, err := os.MkdirTemp("", "olm-catalog-")
	if err != nil {
		return nil, fmt.Errorf("create unpack directory: %w", err)
	}
	defer os.RemoveAll(root)
	if err := registry.Unpack(ctx, ref, root); err != nil {
		return nil, fmt.Errorf("unpack %q: %w", source.Image, err)
	}
	configRoot, err := safeJoin(root, configs)
	if err != nil {
		return nil, fmt.Errorf("catalog config path: %w", err)
	}

	s := &Snapshot{Source: source, GeneratedAt: time.Now().UTC(), Packages: map[string]*Package{}}
	var mu sync.Mutex
	concurrency := r.ParseConcurrency
	if concurrency < 1 {
		concurrency = 2
	}
	err = declcfg.WalkMetasFS(ctx, os.DirFS(configRoot), func(_ string, meta *declcfg.Meta, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		mu.Lock()
		defer mu.Unlock()
		return addMeta(s, meta.Schema, meta.Blob)
	}, declcfg.WithConcurrency(concurrency))
	if err != nil {
		return nil, fmt.Errorf("read FBC: %w", err)
	}
	for _, p := range s.Packages {
		for _, ch := range p.Channels {
			sort.Slice(ch.Entries, func(i, j int) bool { return ch.Entries[i].Name < ch.Entries[j].Name })
		}
	}
	return s, nil
}

func parsePlatform(value string) (osChoice, architectureChoice, variantChoice string, err error) {
	parts := strings.Split(value, "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" || (len(parts) == 3 && parts[2] == "") {
		return "", "", "", fmt.Errorf("invalid platform %q: expected os/architecture[/variant]", value)
	}
	return parts[0], parts[1], strings.Join(parts[2:], ""), nil
}

// ValidatePlatform checks an OCI platform in os/architecture[/variant] form.
func ValidatePlatform(value string) error {
	_, _, _, err := parsePlatform(value)
	return err
}

// safeJoin resolves an OCI image path inside root. FBC images conventionally
// use absolute-looking paths such as /configs; they are absolute only within
// the image filesystem, not on the host.
func safeJoin(root, imagePath string) (string, error) {
	clean := filepath.Clean(strings.TrimLeft(imagePath, "/"))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal is not allowed")
	}
	joined := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes extraction root")
	}
	return joined, nil
}

type rawPackage struct {
	Name           string `json:"name"`
	DefaultChannel string `json:"defaultChannel"`
}
type rawChannel struct {
	Package string  `json:"package"`
	Name    string  `json:"name"`
	Entries []Entry `json:"entries"`
}
type rawBundle struct {
	Name       string        `json:"name"`
	Package    string        `json:"package"`
	Image      string        `json:"image"`
	Properties []rawProperty `json:"properties"`
}
type rawProperty struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

func addMeta(s *Snapshot, schema string, blob []byte) error {
	switch schema {
	case "olm.package":
		var v rawPackage
		if err := json.Unmarshal(blob, &v); err != nil {
			return err
		}
		if v.Name == "" {
			return fmt.Errorf("package metadata has no name")
		}
		p := ensurePackage(s, v.Name)
		p.DefaultChannel = v.DefaultChannel
	case "olm.channel":
		var v rawChannel
		if err := json.Unmarshal(blob, &v); err != nil {
			return err
		}
		if v.Package == "" || v.Name == "" {
			return fmt.Errorf("channel metadata is incomplete")
		}
		p := ensurePackage(s, v.Package)
		p.Channels[v.Name] = &Channel{Name: v.Name, Entries: v.Entries}
	case "olm.bundle":
		var v rawBundle
		if err := json.Unmarshal(blob, &v); err != nil {
			return err
		}
		if v.Package == "" || v.Name == "" {
			return fmt.Errorf("bundle metadata is incomplete")
		}
		p := ensurePackage(s, v.Package)
		p.Bundles[v.Name] = &Bundle{Name: v.Name, Version: packageVersion(v.Properties), Image: v.Image}
	}
	return nil
}

func ensurePackage(s *Snapshot, name string) *Package {
	if p := s.Packages[name]; p != nil {
		return p
	}
	p := &Package{Name: name, Channels: map[string]*Channel{}, Bundles: map[string]*Bundle{}}
	s.Packages[name] = p
	return p
}

func packageVersion(props []rawProperty) string {
	for _, p := range props {
		if p.Type != "olm.package" {
			continue
		}
		var v struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(p.Value, &v) == nil {
			return v.Version
		}
	}
	return ""
}

// Keep fs imported by this package's public behaviour documentation stable.
var _ fs.FS
