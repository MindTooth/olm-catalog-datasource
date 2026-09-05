package openshift

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

const maxChangelogBytes = 512 << 10

var defaultChangelogCache = NewChangelogCache()

type ChangelogCache struct {
	mu      sync.Mutex
	entries map[string]string
	pending map[string]*changelogCall
}

type changelogCall struct {
	done    chan struct{}
	content string
}

func NewChangelogCache() *ChangelogCache {
	return &ChangelogCache{
		entries: make(map[string]string),
		pending: make(map[string]*changelogCall),
	}
}

func (c Client) enrichChangelogs(ctx context.Context, architecture string, releases []Release) {
	// A custom graph does not imply a matching release-controller endpoint.
	// Callers using one can opt in by supplying ChangelogURL explicitly.
	if c.GraphURL != "" && c.GraphURL != DefaultGraphURL && c.ChangelogURL == "" {
		return
	}
	for i := range releases {
		from, ok := previousZStream(releases[i].Version)
		if !ok {
			continue
		}
		content, err := c.changelog(ctx, architecture, from, releases[i].Version)
		if err == nil {
			releases[i].ChangelogContent = content
		}
	}
}

func previousZStream(version string) (string, bool) {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return "", false
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil || patch <= 0 {
		return "", false
	}
	return fmt.Sprintf("%s.%s.%d", parts[0], parts[1], patch-1), true
}

func (c Client) changelog(ctx context.Context, architecture, from, to string) (string, error) {
	cache := c.ChangelogCache
	if cache == nil {
		cache = defaultChangelogCache
	}
	key := architecture + "\x00" + from + "\x00" + to

	cache.mu.Lock()
	if content, ok := cache.entries[key]; ok {
		cache.mu.Unlock()
		return content, nil
	}
	if call, ok := cache.pending[key]; ok {
		cache.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-call.done:
			if call.content == "" {
				return "", fmt.Errorf("fetch OpenShift changelog failed")
			}
			return call.content, nil
		}
	}
	call := &changelogCall{done: make(chan struct{})}
	cache.pending[key] = call
	cache.mu.Unlock()

	content, err := c.fetchChangelog(ctx, architecture, from, to)

	cache.mu.Lock()
	delete(cache.pending, key)
	if err == nil {
		cache.entries[key] = content
		call.content = content
	}
	close(call.done)
	cache.mu.Unlock()
	return content, err
}

func (c Client) fetchChangelog(ctx context.Context, architecture, from, to string) (string, error) {
	base := c.ChangelogURL
	if base == "" {
		base = "https://" + architecture + ".ocp.releases.ci.openshift.org/changelog"
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse OpenShift changelog URL: %w", err)
	}
	q := u.Query()
	q.Set("from", from)
	q.Set("to", to)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("create OpenShift changelog request: %w", err)
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch OpenShift changelog: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		return "", fmt.Errorf("fetch OpenShift changelog: unexpected HTTP status %s", res.Status)
	}
	content, err := io.ReadAll(io.LimitReader(res.Body, maxChangelogBytes+1))
	if err != nil {
		return "", fmt.Errorf("read OpenShift changelog: %w", err)
	}
	if len(content) > maxChangelogBytes {
		return "", fmt.Errorf("OpenShift changelog exceeds %d bytes", maxChangelogBytes)
	}
	if len(content) == 0 {
		return "", fmt.Errorf("OpenShift changelog is empty")
	}
	return string(content), nil
}
