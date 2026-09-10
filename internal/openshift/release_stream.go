package openshift

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/Masterminds/semver/v3"
)

const maxReleaseControllerBytes = 8 << 20

type releaseControllerTags struct {
	Tags []releaseControllerTag `json:"tags"`
}

type releaseControllerTag struct {
	Name  string `json:"name"`
	Phase string `json:"phase"`
}

type releaseStreamRelease struct {
	version  string
	previous string
}

// enrichReleaseStreamChangelogs adds accepted releases that Cincinnati omits as
// deprecated changelog-only entries. Renovate excludes deprecated releases when
// choosing an update, but retains their per-release notes in the changelog
// range. Graph releases stay eligible and retain their graph metadata.
func (c Client) enrichReleaseStreamChangelogs(ctx context.Context, architecture, currentVersion string, releases []Release) []Release {
	if len(releases) < 2 {
		return releases
	}
	// A graph override is often a private or test graph. It does not imply that
	// the public release-controller stream describes the same releases.
	if c.GraphURL != "" && c.GraphURL != DefaultGraphURL && c.ReleaseControllerURL == "" {
		return releases
	}

	ctx, cancel := context.WithTimeout(ctx, changelogFetchTimeout)
	defer cancel()
	baseURL, stream, ok := c.releaseControllerSource(architecture)
	if !ok {
		return releases
	}
	tags, err := c.fetchReleaseControllerTags(ctx, baseURL, stream)
	if err != nil {
		return releases
	}

	limit := latestVersion(releases)
	entries := streamReleasesBetween(tags, currentVersion, limit)
	if len(entries) == 0 {
		return releases
	}

	positions := make(map[string]int, len(releases))
	for i := range releases {
		positions[releases[i].Version] = i
	}
	history := make([]Release, 0, len(entries))
	for _, entry := range entries {
		if i, found := positions[entry.version]; found {
			history = append(history, releases[i])
			continue
		}
		history = append(history, Release{
			Version:      entry.version,
			IsDeprecated: true,
			ChangelogURL: releaseControllerChangelogURL(baseURL, entry.previous, entry.version),
		})
	}

	semaphore := make(chan struct{}, changelogConcurrency)
	var wg sync.WaitGroup
	for i, entry := range entries {
		wg.Add(1)
		go func(index int, item releaseStreamRelease) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			content, err := c.releaseControllerChangelog(ctx, baseURL, item.previous, item.version)
			if err == nil {
				history[index].ChangelogContent = content
			}
		}(i, entry)
	}
	wg.Wait()

	for _, release := range history {
		if i, found := positions[release.Version]; found {
			if release.ChangelogContent != "" {
				releases[i].ChangelogContent = release.ChangelogContent
			}
			continue
		}
		releases = append(releases, release)
	}
	return releases
}

func (c Client) releaseControllerSource(architecture string) (baseURL, stream string, ok bool) {
	baseURL = strings.TrimRight(c.ReleaseControllerURL, "/")
	if baseURL == "" {
		baseURL = DefaultReleaseControllerURL
	}
	switch architecture {
	case "multi":
		return baseURL, "4-stable-multi", true
	case "amd64":
		if c.ReleaseControllerURL == "" {
			baseURL = "https://amd64.ocp.releases.ci.openshift.org"
		}
		return baseURL, "4-stable", true
	case "arm64":
		if c.ReleaseControllerURL == "" {
			baseURL = "https://arm64.ocp.releases.ci.openshift.org"
		}
		return baseURL, "4-stable-arm64", true
	case "ppc64le":
		if c.ReleaseControllerURL == "" {
			baseURL = "https://ppc64le.ocp.releases.ci.openshift.org"
		}
		return baseURL, "4-stable-ppc64le", true
	case "s390x":
		if c.ReleaseControllerURL == "" {
			baseURL = "https://s390x.ocp.releases.ci.openshift.org"
		}
		return baseURL, "4-stable-s390x", true
	default:
		return "", "", false
	}
}

func (c Client) fetchReleaseControllerTags(ctx context.Context, baseURL, stream string) ([]releaseControllerTag, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/api/v1/releasestream/" + url.PathEscape(stream) + "/tags"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create OpenShift release stream request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch OpenShift release stream: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("fetch OpenShift release stream: unexpected HTTP status %s", res.Status)
	}
	var payload releaseControllerTags
	decoder := json.NewDecoder(io.LimitReader(res.Body, maxReleaseControllerBytes+1))
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode OpenShift release stream: %w", err)
	}
	return payload.Tags, nil
}

func streamReleasesBetween(tags []releaseControllerTag, from, to string) []releaseStreamRelease {
	fromVersion, fromErr := semver.StrictNewVersion(from)
	toVersion, toErr := semver.StrictNewVersion(to)
	if fromErr != nil || toErr != nil || !fromVersion.LessThan(toVersion) {
		return nil
	}
	accepted := make([]string, 0, len(tags))
	for _, tag := range tags {
		version, err := semver.StrictNewVersion(tag.Name)
		if tag.Phase == "Accepted" && err == nil && version.Prerelease() == "" {
			accepted = append(accepted, tag.Name)
		}
	}
	sort.Slice(accepted, func(i, j int) bool {
		left, _ := semver.StrictNewVersion(accepted[i])
		right, _ := semver.StrictNewVersion(accepted[j])
		return left.LessThan(right)
	})

	entries := make([]releaseStreamRelease, 0, len(accepted))
	for i, tag := range accepted {
		version, _ := semver.StrictNewVersion(tag)
		if fromVersion.LessThan(version) && !toVersion.LessThan(version) && i > 0 {
			entries = append(entries, releaseStreamRelease{version: tag, previous: accepted[i-1]})
		}
	}
	return entries
}

func latestVersion(releases []Release) string {
	latest := releases[0].Version
	for _, release := range releases[1:] {
		left, leftErr := semver.StrictNewVersion(latest)
		right, rightErr := semver.StrictNewVersion(release.Version)
		if leftErr == nil && rightErr == nil && left.LessThan(right) {
			latest = release.Version
		}
	}
	return latest
}

func releaseControllerChangelogURL(baseURL, from, to string) string {
	u, _ := url.Parse(strings.TrimRight(baseURL, "/") + "/changelog")
	q := u.Query()
	q.Set("from", from)
	q.Set("to", to)
	u.RawQuery = q.Encode()
	return u.String()
}

func (c Client) releaseControllerChangelog(ctx context.Context, baseURL, from, to string) (string, error) {
	key := strings.TrimRight(baseURL, "/") + "\x00release-controller\x00" + from + "\x00" + to
	return c.cachedChangelog(ctx, key, func() (string, error) {
		return c.fetchReleaseControllerChangelog(ctx, baseURL, from, to)
	})
}

func (c Client) fetchReleaseControllerChangelog(ctx context.Context, baseURL, from, to string) (string, error) {
	endpoint := releaseControllerChangelogURL(baseURL, from, to)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("create OpenShift changelog request: %w", err)
	}
	req.Header.Set("Accept", "text/plain")
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
		return "", fmt.Errorf("fetch OpenShift changelog: unexpected HTTP status %s", res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxReleaseControllerBytes+1))
	if err != nil {
		return "", fmt.Errorf("read OpenShift changelog: %w", err)
	}
	if len(body) > maxReleaseControllerBytes {
		return "", fmt.Errorf("OpenShift changelog exceeds %d bytes", maxReleaseControllerBytes)
	}
	content := strings.TrimSpace(string(body))
	if content == "" {
		return "", fmt.Errorf("OpenShift changelog is empty")
	}
	return content + "\n\n_Source: [OpenShift release-controller changelog](" + endpoint + ")._", nil
}
