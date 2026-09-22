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
	version string
}

// addReleaseStreamHistory adds accepted releases that Cincinnati omits as
// deprecated changelog-only entries. Graph releases stay eligible and retain
// their graph metadata; advisory content is fetched separately for all entries.
func (c Client) addReleaseStreamHistory(ctx context.Context, architecture, currentVersion string, advisoryURLs map[string]string, releases []Release) []Release {
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
	for _, entry := range entries {
		if _, found := positions[entry.version]; found {
			continue
		}
		releases = append(releases, Release{
			Version:      entry.version,
			IsDeprecated: true,
			ChangelogURL: advisoryURLs[entry.version],
		})
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

// ReleaseComparisonURL links the installed release to the highest returned
// update without embedding release-controller changelog content.
func (c Client) ReleaseComparisonURL(architecture, from, to string) string {
	if from == "" || to == "" || from == to {
		return ""
	}
	baseURL, stream, ok := c.releaseControllerSource(architecture)
	if !ok {
		return ""
	}
	u, err := url.Parse(strings.TrimRight(baseURL, "/") + "/releasestream/" + url.PathEscape(stream) + "/release/" + url.PathEscape(to))
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("from", from)
	u.RawQuery = q.Encode()
	return u.String()
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
	for _, tag := range accepted {
		version, _ := semver.StrictNewVersion(tag)
		if fromVersion.LessThan(version) && !toVersion.LessThan(version) {
			entries = append(entries, releaseStreamRelease{version: tag})
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
