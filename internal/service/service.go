package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MindTooth/olm-catalog-datasource/internal/catalog"
)

type Config struct {
	Sources          []catalog.Source `yaml:"sources"`
	RefreshInterval  time.Duration    `yaml:"refreshInterval"`
	RefreshTimeout   time.Duration    `yaml:"refreshTimeout"`
	SignaturePolicy  string           `yaml:"signaturePolicy"`
	ParseConcurrency int              `yaml:"parseConcurrency"`
}

type SourceStatus struct {
	Source       catalog.Source `json:"source"`
	Available    bool           `json:"available"`
	Refreshing   bool           `json:"refreshing"`
	LastAttempt  time.Time      `json:"lastAttempt,omitempty"`
	LastSuccess  time.Time      `json:"lastSuccess,omitempty"`
	LastError    string         `json:"lastError,omitempty"`
	PackageCount int            `json:"packageCount"`
}

type Service struct {
	cfg           Config
	mu            sync.RWMutex
	snapshots     map[string]*catalog.Snapshot
	statuses      map[string]SourceStatus
	queued        map[string]bool
	running       map[string]bool
	refreshSem    chan struct{}
	runContext    context.Context
	configChanged chan struct{}
}

func New(cfg Config) *Service {
	return &Service{
		cfg:           cfg,
		snapshots:     map[string]*catalog.Snapshot{},
		statuses:      map[string]SourceStatus{},
		queued:        map[string]bool{},
		running:       map[string]bool{},
		refreshSem:    make(chan struct{}, 1),
		configChanged: make(chan struct{}, 1),
	}
}

func (s *Service) Refresh(ctx context.Context, source catalog.Source) error {
	slog.Debug("refresh catalog", "source", source.ID, "image", source.Image)
	s.markAttempt(source)
	s.mu.RLock()
	r := catalog.Reader{SignaturePolicy: s.cfg.SignaturePolicy, ParseConcurrency: s.cfg.ParseConcurrency}
	s.mu.RUnlock()
	snap, err := r.Read(ctx, source)
	if err != nil {
		s.markFailure(source, err)
		return err
	}
	s.mu.Lock()
	if s.currentSourceLocked(source) {
		s.snapshots[source.ID] = snap
		s.statuses[source.ID] = SourceStatus{Source: source, Available: true, Refreshing: s.running[source.ID] || s.queued[source.ID], LastAttempt: time.Now().UTC(), LastSuccess: snap.GeneratedAt, PackageCount: len(snap.Packages)}
	}
	s.mu.Unlock()
	slog.Debug("catalog refreshed", "source", source.ID, "packages", len(snap.Packages), "generatedAt", snap.GeneratedAt)
	return nil
}

func (s *Service) markAttempt(source catalog.Source) {
	s.mu.Lock()
	if !s.currentSourceLocked(source) {
		s.mu.Unlock()
		return
	}
	status := s.statuses[source.ID]
	status.Source, status.LastAttempt, status.Refreshing = source, time.Now().UTC(), s.running[source.ID] || s.queued[source.ID]
	s.statuses[source.ID] = status
	s.mu.Unlock()
}

func (s *Service) markFailure(source catalog.Source, err error) {
	s.mu.Lock()
	if !s.currentSourceLocked(source) {
		s.mu.Unlock()
		return
	}
	status := s.statuses[source.ID]
	status.Source, status.LastError, status.Refreshing = source, err.Error(), s.running[source.ID] || s.queued[source.ID]
	status.Available = s.snapshots[source.ID] != nil
	s.statuses[source.ID] = status
	s.mu.Unlock()
}

func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	s.runContext = ctx
	s.mu.Unlock()
	s.queueAll()
	for {
		timer := time.NewTimer(s.refreshInterval())
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.queueAll()
		case <-s.configChanged:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
	}
}

func (s *Service) refreshInterval() time.Duration {
	s.mu.RLock()
	interval := s.cfg.RefreshInterval
	s.mu.RUnlock()
	if interval <= 0 {
		return 6 * time.Hour
	}
	return interval
}

// Reload atomically applies a validated configuration. Catalogs that were
// removed or changed are not served until a refresh of their new source wins.
func (s *Service) Reload(cfg Config) {
	s.mu.Lock()
	previous := s.cfg
	changed := sourceChanges(previous, cfg)
	refreshAll := previous.SignaturePolicy != cfg.SignaturePolicy || previous.ParseConcurrency != cfg.ParseConcurrency
	s.cfg = cfg
	for id, snap := range s.snapshots {
		if !s.currentSourceLocked(snap.Source) {
			delete(s.snapshots, id)
		}
	}
	for id, status := range s.statuses {
		if !s.currentSourceLocked(status.Source) {
			delete(s.statuses, id)
		}
	}
	s.mu.Unlock()

	s.notifyConfigChanged()
	if refreshAll {
		s.queueAll()
		return
	}
	for _, id := range changed {
		s.QueueRefresh(id)
	}
}

func sourceChanges(previous, next Config) []string {
	old := sourcesByID(previous.Sources)
	changed := make([]string, 0, len(next.Sources))
	for _, source := range next.Sources {
		if existing, ok := old[source.ID]; !ok || existing != source {
			changed = append(changed, source.ID)
		}
	}
	return changed
}

func sourcesByID(sources []catalog.Source) map[string]catalog.Source {
	result := make(map[string]catalog.Source, len(sources))
	for _, source := range sources {
		result[source.ID] = source
	}
	return result
}

func (s *Service) currentSourceLocked(source catalog.Source) bool {
	for _, configured := range s.cfg.Sources {
		if configured == source {
			return true
		}
	}
	return false
}

func (s *Service) sourceLocked(id string) (catalog.Source, bool) {
	for _, source := range s.cfg.Sources {
		if source.ID == id {
			return source, true
		}
	}
	return catalog.Source{}, false
}

func (s *Service) queueAll() {
	s.mu.RLock()
	ids := make([]string, 0, len(s.cfg.Sources))
	for _, source := range s.cfg.Sources {
		ids = append(ids, source.ID)
	}
	s.mu.RUnlock()
	for _, id := range ids {
		s.QueueRefresh(id)
	}
}

// QueueRefresh starts a background refresh unless this source is already
// queued or running. Its returned state is suitable for a 202 API response.
func (s *Service) QueueRefresh(id string) (state string, found bool) {
	s.mu.Lock()
	source, found := s.sourceLocked(id)
	if !found {
		s.mu.Unlock()
		return "", false
	}
	if s.running[id] {
		s.mu.Unlock()
		return "running", true
	}
	if s.queued[id] {
		s.mu.Unlock()
		return "queued", true
	}
	s.queued[id] = true
	status := s.statuses[id]
	status.Source, status.Refreshing = source, true
	s.statuses[id] = status
	ctx := s.runContext
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Unlock()
	go s.refreshQueued(ctx, id)
	return "queued", true
}

func (s *Service) refreshQueued(parent context.Context, id string) {
	select {
	case s.refreshSem <- struct{}{}:
	case <-parent.Done():
		s.finishQueued(id)
		return
	}
	defer func() { <-s.refreshSem }()

	s.mu.Lock()
	delete(s.queued, id)
	source, found := s.sourceLocked(id)
	if found {
		s.running[id] = true
	}
	s.mu.Unlock()
	if !found {
		s.finishQueued(id)
		return
	}

	s.mu.RLock()
	timeout := s.cfg.RefreshTimeout
	s.mu.RUnlock()
	ctx, cancel := parent, func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, timeout)
	}
	if err := s.Refresh(ctx, source); err != nil {
		slog.Error("refresh catalog", "source", source.ID, "error", err)
	}
	cancel()

	s.mu.Lock()
	delete(s.running, id)
	if status, ok := s.statuses[id]; ok {
		status.Refreshing = false
		s.statuses[id] = status
	}
	changed := !s.currentSourceLocked(source)
	s.mu.Unlock()
	if changed {
		s.QueueRefresh(id)
	}
}

func (s *Service) finishQueued(id string) {
	s.mu.Lock()
	delete(s.queued, id)
	delete(s.running, id)
	if status, ok := s.statuses[id]; ok {
		status.Refreshing = false
		s.statuses[id] = status
	}
	s.mu.Unlock()
}

func (s *Service) notifyConfigChanged() {
	select {
	case s.configChanged <- struct{}{}:
	default:
	}
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", s.ready)
	mux.HandleFunc("/v1/refresh", s.refreshAllHTTP)
	mux.HandleFunc("/v1/catalogs", s.catalog)
	mux.HandleFunc("/v1/catalogs/", s.catalog)
	return accessLog(mux)
}

func (s *Service) ready(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	ready := len(s.snapshots) > 0
	s.mu.RUnlock()
	if !ready {
		http.Error(w, "no catalog has completed refresh", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Service) catalog(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(path.Clean(r.URL.Path), "/"), "/")
	if len(parts) < 2 || parts[0] != "v1" || parts[1] != "catalogs" {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 4 && parts[3] == "refresh" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.refreshHTTP(w, r, parts[2])
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if len(parts) == 2 {
		s.listCatalogs(w)
		return
	}
	sourceID := parts[2]
	if len(parts) == 4 && parts[3] == "status" {
		s.catalogStatus(w, sourceID)
		return
	}
	if len(parts) == 4 && parts[3] == "packages" {
		s.listPackages(w, r, sourceID)
		return
	}
	if len(parts) != 6 || parts[3] != "packages" {
		http.NotFound(w, r)
		return
	}
	snap := s.snapshot(w, sourceID)
	if snap == nil {
		return
	}
	pkg := snap.Packages[parts[4]]
	if pkg == nil {
		http.NotFound(w, r)
		return
	}
	switch parts[5] {
	case "updates", "channel-updates":
		s.renovate(w, r, pkg, parts[5])
	case "channel-releases":
		s.channelReleases(w, r, pkg)
	case "channels":
		s.channels(w, r, pkg)
	case "bundles":
		s.bundles(w, r, pkg)
	case "graph":
		s.graph(w, r, pkg)
	case "resolve":
		s.resolve(w, r, pkg)
	default:
		http.NotFound(w, r)
	}
}

func (s *Service) refreshAllHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	ids := make([]string, 0, len(s.cfg.Sources))
	for _, source := range s.cfg.Sources {
		ids = append(ids, source.ID)
	}
	s.mu.RUnlock()
	type accepted struct {
		Source string `json:"source"`
		State  string `json:"state"`
	}
	results := make([]accepted, 0, len(ids))
	for _, id := range ids {
		state, _ := s.QueueRefresh(id)
		results = append(results, accepted{Source: id, State: state})
	}
	writeJSON(w, http.StatusAccepted, struct {
		Accepted bool       `json:"accepted"`
		Sources  []accepted `json:"sources"`
	}{Accepted: true, Sources: results})
}

func (s *Service) refreshHTTP(w http.ResponseWriter, _ *http.Request, sourceID string) {
	state, found := s.QueueRefresh(sourceID)
	if !found {
		http.Error(w, "catalog is not configured", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		Accepted bool   `json:"accepted"`
		Source   string `json:"source"`
		State    string `json:"state"`
	}{Accepted: true, Source: sourceID, State: state})
}

func (s *Service) snapshot(w http.ResponseWriter, sourceID string) *catalog.Snapshot {
	s.mu.RLock()
	snap := s.snapshots[sourceID]
	s.mu.RUnlock()
	if snap == nil {
		http.Error(w, "catalog is unavailable", http.StatusServiceUnavailable)
	}
	return snap
}

func (s *Service) listCatalogs(w http.ResponseWriter) {
	statuses := make([]SourceStatus, 0, len(s.cfg.Sources))
	s.mu.RLock()
	for _, source := range s.cfg.Sources {
		status, ok := s.statuses[source.ID]
		if !ok {
			status.Source = source
		}
		statuses = append(statuses, status)
	}
	s.mu.RUnlock()
	writeJSON(w, http.StatusOK, struct {
		Catalogs []SourceStatus `json:"catalogs"`
	}{statuses})
}

func (s *Service) catalogStatus(w http.ResponseWriter, sourceID string) {
	s.mu.RLock()
	status, ok := s.statuses[sourceID]
	s.mu.RUnlock()
	if !ok {
		for _, source := range s.cfg.Sources {
			if source.ID == sourceID {
				writeJSON(w, http.StatusOK, SourceStatus{Source: source})
				return
			}
		}
		http.Error(w, "catalog is not configured", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Service) listPackages(w http.ResponseWriter, r *http.Request, sourceID string) {
	snap := s.snapshot(w, sourceID)
	if snap == nil {
		return
	}
	prefix, limit := r.URL.Query().Get("prefix"), queryLimit(r, 100, 1000)
	type item struct {
		Name           string `json:"name"`
		DefaultChannel string `json:"defaultChannel"`
		Channels       int    `json:"channels"`
		Bundles        int    `json:"bundles"`
	}
	items := make([]item, 0, limit)
	for _, name := range sortedPackageNames(snap.Packages) {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		p := snap.Packages[name]
		items = append(items, item{Name: p.Name, DefaultChannel: p.DefaultChannel, Channels: len(p.Channels), Bundles: len(p.Bundles)})
		if len(items) == limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Packages []item `json:"packages"`
		Limit    int    `json:"limit"`
	}{items, limit})
}

func (s *Service) renovate(w http.ResponseWriter, r *http.Request, p *catalog.Package, action string) {
	var values []string
	var err error
	if action == "channel-updates" {
		values, err = p.ChannelUpdates(catalog.ChannelUpdateRequest{CurrentChannel: r.URL.Query().Get("currentChannel"), CurrentVersion: r.URL.Query().Get("currentVersion"), CurrentBundle: r.URL.Query().Get("currentBundle"), Selection: r.URL.Query().Get("selection")})
	} else {
		values, err = p.VersionUpdates(catalog.UpdateRequest{CurrentVersion: r.URL.Query().Get("currentVersion"), CurrentBundle: r.URL.Query().Get("currentBundle"), Channel: r.URL.Query().Get("channel"), Mode: r.URL.Query().Get("mode")})
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Releases []release `json:"releases"`
	}{releases(values)})
}

type release struct {
	Version string `json:"version"`
	Bundle  string `json:"bundle,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

func releases(values []string) []release {
	out := make([]release, len(values))
	for i, value := range values {
		out[i].Version = value
	}
	return out
}

// channelReleases is the Renovate-oriented, graph-safe channel endpoint. Its
// digest field carries the opaque target bundle name so Renovate can persist it
// as currentDigest and supply it on the next lookup.
func (s *Service) channelReleases(w http.ResponseWriter, r *http.Request, p *catalog.Package) {
	values, err := p.ChannelReleases(catalog.ChannelUpdateRequest{
		CurrentChannel: r.URL.Query().Get("currentChannel"),
		CurrentVersion: r.URL.Query().Get("currentVersion"),
		CurrentBundle:  r.URL.Query().Get("currentBundle"),
		Selection:      r.URL.Query().Get("selection"),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	// The first item represents the installed state. A datasource should expose
	// only update candidates; Renovate already knows its current value.
	out := make([]release, 0, max(len(values)-1, 0))
	for _, value := range values[1:] {
		out = append(out, release{Version: value.Channel, Digest: value.Bundle})
	}
	writeJSON(w, http.StatusOK, struct {
		Releases []release `json:"releases"`
	}{out})
}

func (s *Service) channels(w http.ResponseWriter, r *http.Request, p *catalog.Package) {
	type channel struct {
		Name       string          `json:"name"`
		Deprecated bool            `json:"deprecated"`
		Entries    int             `json:"entries"`
		Heads      []release       `json:"heads"`
		Graph      []catalog.Entry `json:"graph,omitempty"`
	}
	includeEntries := strings.Contains(r.URL.Query().Get("include"), "entries")
	names, out := sortedChannelNames(p.Channels), make([]channel, 0, len(p.Channels))
	for _, name := range names {
		ch := p.Channels[name]
		heads := p.ChannelHeads(name)
		view := channel{Name: name, Deprecated: ch.Deprecated, Entries: len(ch.Entries), Heads: make([]release, len(heads))}
		for i, head := range heads {
			view.Heads[i] = release{Bundle: head.Name, Version: head.Version}
		}
		if includeEntries {
			view.Graph = ch.Entries
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, struct {
		Package        string    `json:"package"`
		DefaultChannel string    `json:"defaultChannel"`
		Channels       []channel `json:"channels"`
	}{p.Name, p.DefaultChannel, out})
}

func (s *Service) bundles(w http.ResponseWriter, r *http.Request, p *catalog.Package) {
	channel := r.URL.Query().Get("channel")
	allowed := map[string]bool{}
	if channel != "" {
		ch := p.Channels[channel]
		if ch == nil {
			http.Error(w, "unknown channel", http.StatusNotFound)
			return
		}
		for _, entry := range ch.Entries {
			allowed[entry.Name] = true
		}
	}
	type bundle struct {
		Name       string `json:"name"`
		Version    string `json:"version"`
		Image      string `json:"image,omitempty"`
		Deprecated bool   `json:"deprecated"`
	}
	names := make([]string, 0, len(p.Bundles))
	for name := range p.Bundles {
		if channel == "" || allowed[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := make([]bundle, 0, len(names))
	for _, name := range names {
		b := p.Bundles[name]
		out = append(out, bundle{Name: b.Name, Version: b.Version, Image: b.Image, Deprecated: b.Deprecated})
	}
	writeJSON(w, http.StatusOK, struct {
		Package string   `json:"package"`
		Bundles []bundle `json:"bundles"`
	}{p.Name, out})
}

func (s *Service) graph(w http.ResponseWriter, r *http.Request, p *catalog.Package) {
	requested := r.URL.Query().Get("channel")
	names := sortedChannelNames(p.Channels)
	type graphChannel struct {
		Name    string          `json:"name"`
		Entries []catalog.Entry `json:"entries"`
	}
	out := []graphChannel{}
	for _, name := range names {
		if requested != "" && name != requested {
			continue
		}
		out = append(out, graphChannel{Name: name, Entries: p.Channels[name].Entries})
	}
	if requested != "" && len(out) == 0 {
		http.Error(w, "unknown channel", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Package  string         `json:"package"`
		Channels []graphChannel `json:"channels"`
	}{p.Name, out})
}

func (s *Service) resolve(w http.ResponseWriter, r *http.Request, p *catalog.Package) {
	var candidates []string
	var err error
	kind := r.URL.Query().Get("kind")
	if kind == "channel" {
		candidates, err = p.ChannelUpdates(catalog.ChannelUpdateRequest{CurrentChannel: r.URL.Query().Get("currentChannel"), CurrentVersion: r.URL.Query().Get("currentVersion"), CurrentBundle: r.URL.Query().Get("currentBundle"), Selection: r.URL.Query().Get("selection")})
	} else {
		candidates, err = p.VersionUpdates(catalog.UpdateRequest{CurrentVersion: r.URL.Query().Get("currentVersion"), CurrentBundle: r.URL.Query().Get("currentBundle"), Channel: r.URL.Query().Get("channel"), Mode: r.URL.Query().Get("mode")})
	}
	response := struct {
		Kind       string   `json:"kind"`
		Candidates []string `json:"candidates"`
		Valid      bool     `json:"valid"`
		Reason     string   `json:"reason,omitempty"`
	}{Kind: kind, Candidates: candidates, Valid: err == nil && len(candidates) > 0}
	if err != nil {
		response.Reason = err.Error()
	} else if len(candidates) == 0 {
		response.Reason = "no graph-valid update path"
	}
	writeJSON(w, http.StatusOK, response)
}

func queryLimit(r *http.Request, fallback, maximum int) int {
	value, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || value < 1 {
		return fallback
	}
	if value > maximum {
		return maximum
	}
	return value
}
func sortedPackageNames(packages map[string]*catalog.Package) []string {
	names := make([]string, 0, len(packages))
	for name := range packages {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func sortedChannelNames(channels map[string]*catalog.Channel) []string {
	names := make([]string, 0, len(channels))
	for name := range channels {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return catalog.ChannelLess(names[i], names[j]) })
	return names
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Error("encode response", "error", err)
	}
}

func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &responseRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		attrs := []any{"method", r.Method, "path", r.URL.Path, "status", recorder.statusCode(), "bytes", recorder.bytes, "duration", time.Since(started), "remoteAddr", r.RemoteAddr}
		if slog.Default().Enabled(r.Context(), slog.LevelDebug) {
			attrs = append(attrs, "query", r.URL.RawQuery, "userAgent", r.UserAgent())
		}
		slog.InfoContext(r.Context(), "http request", attrs...)
	})
}

type responseRecorder struct {
	http.ResponseWriter
	status, bytes int
}

func (w *responseRecorder) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *responseRecorder) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += n
	return n, err
}
func (w *responseRecorder) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}
