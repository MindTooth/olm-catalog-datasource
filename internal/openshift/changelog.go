package openshift

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
)

const (
	DefaultErrataURL            = "https://access.redhat.com/errata"
	maxErrataBytes              = 8 << 20
	maxChangelogBytes           = 6_000
	maxAggregateChangelogBytes  = 24_000
	changelogCacheTTL           = 6 * time.Hour
	changelogFetchTimeout       = 5 * time.Second
	changelogConcurrency        = 4
	shortenedNoticeReserve      = 1024
)

var (
	advisoryIDPattern       = regexp.MustCompile(`^(RH(?:SA|BA|EA)-[0-9]{4}:[0-9]+)$`)
	advisoryIDSearchPattern = regexp.MustCompile(`RH(?:SA|BA|EA)-[0-9]{4}:[0-9]+`)
	cvePattern              = regexp.MustCompile(`CVE-[0-9]{4}-[0-9]+`)
	issuePattern            = regexp.MustCompile(`(?:OCPBUGS-[0-9]+|BZ\s*-\s*[0-9]+|[A-Z][A-Z0-9]+-[0-9]+)`)
	datePattern             = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

	defaultChangelogCache = NewChangelogCache()
)

type ChangelogCache struct {
	mu      sync.Mutex
	entries map[string]changelogEntry
	pending map[string]*changelogCall
}

type changelogEntry struct {
	content   string
	fetchedAt time.Time
}

type changelogCall struct {
	done    chan struct{}
	content string
	err     error
}

type advisorySummary struct {
	ID          string
	URL         string
	Issued      string
	Updated     string
	Synopsis    string
	Type        string
	Severity    string
	Issues      []advisoryReference
	CVEs        []advisoryReference
	Related     []advisoryReference
	ReleaseDocs []advisoryReference
}

type advisoryReference struct {
	ID    string
	Title string
	URL   string
}

func NewChangelogCache() *ChangelogCache {
	return &ChangelogCache{
		entries: make(map[string]changelogEntry),
		pending: make(map[string]*changelogCall),
	}
}

func (c Client) enrichChangelogs(ctx context.Context, releases []Release) {
	ctx, cancel := context.WithTimeout(ctx, changelogFetchTimeout)
	defer cancel()

	semaphore := make(chan struct{}, changelogConcurrency)
	var wg sync.WaitGroup
	for i := range releases {
		advisoryID, ok := advisoryIDFromURL(releases[i].ChangelogURL)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(index int, id string) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			content, err := c.changelog(ctx, id, releases[index].ChangelogURL)
			if err == nil {
				releases[index].ChangelogContent = content
			}
		}(i, advisoryID)
	}
	wg.Wait()

	remaining := maxAggregateChangelogBytes
	for i := len(releases) - 1; i >= 0; i-- {
		content := releases[i].ChangelogContent
		if content == "" {
			continue
		}
		if len(content) > remaining {
			fallbackURL := DefaultErrataURL
			if id, ok := advisoryIDFromURL(releases[i].ChangelogURL); ok {
				fallbackURL += "/" + id
			}
			fallback := fmt.Sprintf("### Release advisory summary\n\n_Summary omitted to keep aggregated release notes within limits. See the [full advisory](%s)._", fallbackURL)
			if len(fallback) <= remaining {
				releases[i].ChangelogContent = fallback
				remaining -= len(fallback)
			} else {
				releases[i].ChangelogContent = ""
			}
			continue
		}
		remaining -= len(content)
	}
}

func advisoryIDFromURL(rawURL string) (string, bool) {
	u, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || !strings.EqualFold(u.Hostname(), "access.redhat.com") {
		return "", false
	}
	id := strings.TrimPrefix(strings.TrimRight(u.Path, "/"), "/errata/")
	if strings.Contains(id, "/") || !advisoryIDPattern.MatchString(id) {
		return "", false
	}
	return id, true
}

func (c Client) changelog(ctx context.Context, advisoryID, canonicalURL string) (string, error) {
	base := strings.TrimRight(c.ErrataURL, "/")
	if base == "" {
		base = DefaultErrataURL
	}
	key := base + "\x00" + advisoryID
	cache := c.ChangelogCache
	if cache == nil {
		cache = defaultChangelogCache
	}
	now := time.Now()

	cache.mu.Lock()
	if cache.entries == nil {
		cache.entries = make(map[string]changelogEntry)
	}
	if cache.pending == nil {
		cache.pending = make(map[string]*changelogCall)
	}
	stale, hasStale := cache.entries[key]
	if hasStale && now.Sub(stale.fetchedAt) < changelogCacheTTL {
		cache.mu.Unlock()
		return stale.content, nil
	}
	if call, ok := cache.pending[key]; ok {
		cache.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-call.done:
			return call.content, call.err
		}
	}
	call := &changelogCall{done: make(chan struct{})}
	cache.pending[key] = call
	cache.mu.Unlock()

	canonicalURL = DefaultErrataURL + "/" + advisoryID
	content, fetchErr := c.fetchChangelog(ctx, base, advisoryID, canonicalURL)
	err := fetchErr
	if fetchErr != nil && hasStale {
		content, err = stale.content, nil
	}

	cache.mu.Lock()
	delete(cache.pending, key)
	if fetchErr == nil {
		cache.entries[key] = changelogEntry{content: content, fetchedAt: now}
	}
	call.content, call.err = content, err
	close(call.done)
	cache.mu.Unlock()
	return content, err
}

func (c Client) fetchChangelog(ctx context.Context, base, advisoryID, canonicalURL string) (string, error) {
	fetchURL := base + "/" + advisoryID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return "", fmt.Errorf("create Red Hat advisory request: %w", err)
	}
	req.Header.Set("Accept", "text/html")
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch Red Hat advisory: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		return "", fmt.Errorf("fetch Red Hat advisory: unexpected HTTP status %s", res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxErrataBytes+1))
	if err != nil {
		return "", fmt.Errorf("read Red Hat advisory: %w", err)
	}
	if len(body) > maxErrataBytes {
		return "", fmt.Errorf("Red Hat advisory exceeds %d bytes", maxErrataBytes)
	}
	summary, err := parseAdvisoryHTML(strings.NewReader(string(body)), canonicalURL)
	if err != nil {
		return "", err
	}
	if summary.ID != advisoryID {
		return "", fmt.Errorf("Red Hat advisory ID %q does not match requested %q", summary.ID, advisoryID)
	}
	return renderAdvisoryMarkdown(summary, maxChangelogBytes), nil
}

func parseAdvisoryHTML(r io.Reader, canonicalURL string) (advisorySummary, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return advisorySummary{}, fmt.Errorf("parse Red Hat advisory HTML: %w", err)
	}

	summary := advisorySummary{URL: canonicalURL}
	sections := make(map[string][]*html.Node)
	var currentSection string
	var textTokens []string
	var allLinks []advisoryReference
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			if text := normalizeSpace(n.Data); text != "" {
				textTokens = append(textTokens, text)
			}
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "h1":
				if match := advisoryIDSearchPattern.FindString(normalizeSpace(textContent(n))); match != "" {
					summary.ID = match
				}
			case "h2":
				currentSection = normalizeSpace(textContent(n))
			case "p", "li":
				if currentSection != "" {
					sections[currentSection] = append(sections[currentSection], n)
				}
			case "a":
				if href := safeReferenceURL(attr(n, "href")); href != "" {
					allLinks = append(allLinks, advisoryReference{Title: normalizeSpace(textContent(n)), URL: href})
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)

	if summary.ID == "" {
		return advisorySummary{}, errors.New("parse Red Hat advisory HTML: advisory identifier not found")
	}
	summary.Issued = valueAfterToken(textTokens, "Issued:")
	summary.Updated = valueAfterToken(textTokens, "Updated:")
	summary.Synopsis = truncateText(firstSectionText(sections, "Synopsis"), 500)
	typeSeverity := firstSectionText(sections, "Type/Severity")
	if typeSeverity == "" || summary.Synopsis == "" {
		return advisorySummary{}, errors.New("parse Red Hat advisory HTML: required advisory sections not found")
	}
	summary.Type, summary.Severity = splitTypeSeverity(typeSeverity)
	summary.Issues = referencesFromSection(sections["Fixes"], issuePattern)
	summary.CVEs = referencesFromSection(sections["CVEs"], cvePattern)
	summary.Related = relatedAdvisories(allLinks, summary.ID)
	summary.ReleaseDocs = releaseDocumentation(allLinks)
	return summary, nil
}

func firstSectionText(sections map[string][]*html.Node, name string) string {
	for _, node := range sections[name] {
		if text := normalizeSpace(textContent(node)); text != "" {
			return text
		}
	}
	return ""
}

func referencesFromSection(nodes []*html.Node, pattern *regexp.Regexp) []advisoryReference {
	seen := make(map[string]bool)
	var refs []advisoryReference
	for _, node := range nodes {
		text := normalizeSpace(textContent(node))
		id := pattern.FindString(text)
		if id == "" {
			continue
		}
		id = strings.ReplaceAll(id, " ", "")
		if seen[id] {
			continue
		}
		seen[id] = true
		linkText, href := firstLink(node)
		title := truncateText(strings.TrimSpace(text), 300)
		if linkText != "" {
			title = strings.TrimSpace(strings.TrimPrefix(title, linkText))
			title = strings.TrimSpace(strings.TrimPrefix(title, "-"))
		}
		refs = append(refs, advisoryReference{ID: id, Title: title, URL: href})
	}
	return refs
}

func relatedAdvisories(links []advisoryReference, self string) []advisoryReference {
	seen := map[string]bool{self: true}
	var refs []advisoryReference
	for _, link := range links {
		id := advisoryIDSearchPattern.FindString(link.Title)
		if id == "" {
			if u, err := url.Parse(link.URL); err == nil {
				id = advisoryIDSearchPattern.FindString(strings.TrimRight(u.Path, "/"))
			}
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		refs = append(refs, advisoryReference{ID: id, URL: link.URL})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	return refs
}

func releaseDocumentation(links []advisoryReference) []advisoryReference {
	seen := make(map[string]bool)
	var refs []advisoryReference
	for _, link := range links {
		u, err := url.Parse(link.URL)
		if err != nil || !strings.EqualFold(u.Hostname(), "docs.redhat.com") || !strings.Contains(u.Path, "/release_notes") || seen[link.URL] {
			continue
		}
		seen[link.URL] = true
		refs = append(refs, advisoryReference{Title: "OpenShift release notes", URL: link.URL})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].URL < refs[j].URL })
	return refs
}

func renderAdvisoryMarkdown(summary advisorySummary, limit int) string {
	var b strings.Builder
	shortened := false
	appendRequired := func(line string) {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	appendOptional := func(line string) bool {
		needed := len(line)
		if b.Len() > 0 {
			needed++
		}
		if b.Len()+needed > limit-shortenedNoticeReserve {
			shortened = true
			return false
		}
		appendRequired(line)
		return true
	}

	appendRequired("### Release advisory summary")
	appendRequired("")
	appendRequired(fmt.Sprintf("[%s](%s) — %s", summary.ID, summary.URL, summary.Synopsis))
	meta := "**Type:** " + summary.Type
	if summary.Severity != "" {
		meta += " · **Severity:** " + summary.Severity
	}
	if summary.Issued != "" {
		meta += " · **Issued:** " + summary.Issued
	}
	if summary.Updated != "" && summary.Updated != summary.Issued {
		meta += " · **Updated:** " + summary.Updated
	}
	appendRequired(meta)

	if len(summary.Issues) > 0 {
		appendRequired("")
		appendRequired("**Fixes**")
		for _, ref := range summary.Issues {
			if !appendOptional("- " + markdownReference(ref)) {
				break
			}
		}
	}
	if len(summary.CVEs) > 0 {
		appendRequired("")
		appendRequired("**CVEs**")
		for _, ref := range summary.CVEs {
			if !appendOptional("- " + markdownReference(ref)) {
				break
			}
		}
	}
	if len(summary.Related) > 0 || len(summary.ReleaseDocs) > 0 {
		appendRequired("")
		appendRequired("**Related**")
		for _, ref := range summary.Related {
			if !appendOptional("- " + markdownReference(ref)) {
				break
			}
		}
		for _, ref := range summary.ReleaseDocs {
			if !appendOptional("- " + markdownReference(ref)) {
				break
			}
		}
	}
	appendRequired("")
	if shortened {
		appendRequired(fmt.Sprintf("_Summary shortened to fit embedded release-note limits. See the [full advisory](%s)._", summary.URL))
	} else {
		appendRequired(fmt.Sprintf("_Source: [Red Hat advisory](%s). This is a release advisory summary, not a complete source changelog._", summary.URL))
	}
	return b.String()
}

func markdownReference(ref advisoryReference) string {
	label := ref.ID
	if label == "" {
		label = ref.Title
	}
	if ref.URL != "" {
		label = fmt.Sprintf("[%s](%s)", label, ref.URL)
	}
	if ref.Title != "" && ref.Title != ref.ID {
		label += " — " + ref.Title
	}
	return label
}

func splitTypeSeverity(value string) (string, string) {
	parts := strings.SplitN(value, ":", 2)
	if len(parts) == 1 {
		return strings.TrimSpace(value), ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func valueAfterToken(tokens []string, label string) string {
	for i, token := range tokens {
		if token != label {
			continue
		}
		for j := i + 1; j < len(tokens); j++ {
			if datePattern.MatchString(tokens[j]) {
				return tokens[j]
			}
			if strings.HasSuffix(tokens[j], ":") {
				break
			}
		}
	}
	return ""
}

func firstLink(n *html.Node) (string, string) {
	if n.Type == html.ElementNode && n.Data == "a" {
		return normalizeSpace(textContent(n)), safeReferenceURL(attr(n, "href"))
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if text, href := firstLink(child); href != "" {
			return text, href
		}
	}
	return "", ""
}

func attr(n *html.Node, name string) string {
	for _, attribute := range n.Attr {
		if attribute.Key == name {
			return attribute.Val
		}
	}
	return ""
}

func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
			b.WriteByte(' ')
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return b.String()
}

func safeReferenceURL(rawURL string) string {
	if strings.HasPrefix(rawURL, "/") {
		return "https://access.redhat.com" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return rawURL
}

func truncateText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	if limit <= 0 {
		return ""
	}
	const ellipsis = "…"
	if limit <= len(ellipsis) {
		cut := limit
		for cut > 0 && !utf8.RuneStart(value[cut]) {
			cut--
		}
		return strings.TrimSpace(value[:cut])
	}
	cut := limit - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return strings.TrimSpace(value[:cut]) + ellipsis
}

func normalizeSpace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
