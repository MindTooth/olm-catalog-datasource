package openshift

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/Masterminds/semver/v3"
	"golang.org/x/net/html"
)

const maxReleaseStreamBytes = 4 << 20

type releaseHistoryEntry struct {
	version     string
	releaseURL  string
	advisoryURL string
}

// enrichChangelogHistory keeps update eligibility in Cincinnati, but builds
// each target's notes from every accepted release in (currentVersion, target].
func (c Client) enrichChangelogHistory(ctx context.Context, architecture, currentVersion string, graphNodes []node, targets []Release) {
	if len(targets) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, changelogFetchTimeout)
	defer cancel()

	streamURL, ok := c.releaseStreamURL(architecture)
	if !ok {
		return
	}
	entries, err := c.fetchReleaseStream(ctx, streamURL)
	if err != nil {
		return
	}
	graphAdvisories := make(map[string]string, len(graphNodes))
	for _, n := range graphNodes {
		if _, ok := advisoryIDFromURL(n.Metadata["url"]); ok {
			graphAdvisories[n.Version] = n.Metadata["url"]
		}
	}
	for i := range targets {
		history := releasesBetween(entries, currentVersion, targets[i].Version)
		for j := range history {
			history[j].advisoryURL = graphAdvisories[history[j].version]
		}
		c.resolveAdvisoryURLs(ctx, history)
		targets[i].ChangelogContent = c.renderReleaseHistory(ctx, history)
	}
}

func (c Client) releaseStreamURL(architecture string) (string, bool) {
	if c.ReleaseControllerURL != "" {
		return strings.TrimRight(c.ReleaseControllerURL, "/"), true
	}
	switch architecture {
	case "multi":
		return "https://multi.ocp.releases.ci.openshift.org/releasestream/4-stable-multi", true
	case "amd64":
		return "https://amd64.ocp.releases.ci.openshift.org/releasestream/4-stable", true
	case "arm64":
		return "https://arm64.ocp.releases.ci.openshift.org/releasestream/4-stable-arm64", true
	default:
		return "", false
	}
}

func (c Client) fetchReleaseStream(ctx context.Context, streamURL string) ([]releaseHistoryEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return nil, err
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("release stream returned %s", res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxReleaseStreamBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read release stream: %w", err)
	}
	if len(body) > maxReleaseStreamBytes {
		return nil, errors.New("release stream exceeds size limit")
	}
	return parseReleaseStream(strings.NewReader(string(body)), streamURL)
}

func parseReleaseStream(r io.Reader, streamURL string) ([]releaseHistoryEntry, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, err
	}
	base, err := url.Parse(streamURL)
	if err != nil {
		return nil, err
	}
	var entries []releaseHistoryEntry
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "tr" {
			cells := tableCells(n)
			if len(cells) >= 2 && strings.HasPrefix(cells[1], "Accepted") {
				version, href := firstLink(n)
				if v, err := semver.StrictNewVersion(version); err == nil && v.Prerelease() == "" && href != "" {
					if u, err := base.Parse(href); err == nil {
						entries = append(entries, releaseHistoryEntry{version: version, releaseURL: u.String()})
					}
				}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	sort.Slice(entries, func(i, j int) bool {
		left, _ := semver.StrictNewVersion(entries[i].version)
		right, _ := semver.StrictNewVersion(entries[j].version)
		return left.LessThan(right)
	})
	return entries, nil
}

func tableCells(row *html.Node) []string {
	var cells []string
	for child := row.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && (child.Data == "td" || child.Data == "th") {
			cells = append(cells, normalizeSpace(textContent(child)))
		}
	}
	return cells
}

func releasesBetween(entries []releaseHistoryEntry, from, to string) []releaseHistoryEntry {
	fromVersion, fromErr := semver.StrictNewVersion(from)
	toVersion, toErr := semver.StrictNewVersion(to)
	if fromErr != nil || toErr != nil || !fromVersion.LessThan(toVersion) {
		return nil
	}
	var result []releaseHistoryEntry
	for _, entry := range entries {
		version, err := semver.StrictNewVersion(entry.version)
		if err == nil && fromVersion.LessThan(version) && !toVersion.LessThan(version) {
			result = append(result, entry)
		}
	}
	return result
}

func (c Client) resolveAdvisoryURLs(ctx context.Context, entries []releaseHistoryEntry) {
	semaphore := make(chan struct{}, changelogConcurrency)
	var wg sync.WaitGroup
	for i := range entries {
		if entries[i].advisoryURL != "" {
			continue
		}
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			entries[index].advisoryURL, _ = c.fetchReleaseAdvisoryURL(ctx, entries[index].releaseURL)
		}(i)
	}
	wg.Wait()
}

func (c Client) fetchReleaseAdvisoryURL(ctx context.Context, releaseURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL, nil)
	if err != nil {
		return "", err
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("release page returned %s", res.Status)
	}
	doc, err := html.Parse(io.LimitReader(res.Body, maxReleaseStreamBytes))
	if err != nil {
		return "", err
	}
	var found string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != "" {
			return
		}
		if n.Type == html.ElementNode && n.Data == "a" {
			href := attr(n, "href")
			if _, ok := advisoryIDFromURL(href); ok {
				found = href
				return
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	if found == "" {
		return "", errors.New("Red Hat advisory link not found")
	}
	return found, nil
}

func (c Client) renderReleaseHistory(ctx context.Context, entries []releaseHistoryEntry) string {
	remaining := maxAggregateChangelogBytes
	sections := make([]string, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		var body string
		if id, ok := advisoryIDFromURL(entry.advisoryURL); ok {
			body, _ = c.changelog(ctx, id, entry.advisoryURL)
		}
		if body == "" {
			body = fmt.Sprintf("[OpenShift %s release details](%s)", entry.version, entry.releaseURL)
		}
		section := fmt.Sprintf("## OpenShift %s\n\n%s", entry.version, body)
		if len(section) > remaining {
			section = fmt.Sprintf("## OpenShift %s\n\n_Summary omitted to keep the complete release history within embedded-note limits. See the [release details](%s)._", entry.version, entry.releaseURL)
		}
		if len(section) > remaining {
			break
		}
		sections = append(sections, section)
		remaining -= len(section) + 2
	}
	return strings.Join(sections, "\n\n")
}
