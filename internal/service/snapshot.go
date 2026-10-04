package service

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/MindTooth/ratatoskr/internal/openshift"
)

// OpenShift lookups depend on client input. Bound retained results rather than
// allowing arbitrary channels and installed versions to grow memory indefinitely.
const maxOpenShiftSnapshots = 128

type openShiftSnapshot struct {
	releases    []openshift.Release
	generatedAt time.Time
	attempt     uint64
}

func (s *Service) snapshotAcceptableLocked(generatedAt time.Time) bool {
	maxAge := s.cfg.MaxSnapshotAge
	if maxAge <= 0 {
		maxAge = DefaultMaxSnapshotAge
	}
	return !generatedAt.IsZero() && s.now().Sub(generatedAt) <= maxAge
}

func (s *Service) freshStatusLocked(status SourceStatus) SourceStatus {
	snap := s.snapshots[status.Source.ID]
	status.Available = snap != nil && s.snapshotAcceptableLocked(snap.GeneratedAt)
	status.Stale = snap != nil && !status.Available
	if snap != nil {
		status.LastSuccess = snap.GeneratedAt
		status.SnapshotAgeSeconds = max(0, s.now().Sub(snap.GeneratedAt).Seconds())
		status.PackageCount = len(snap.Packages)
	}
	return status
}

func setSnapshotHeaders(w http.ResponseWriter, generatedAt, now time.Time) {
	w.Header().Set("X-Ratatoskr-Generated-At", generatedAt.Format(time.RFC3339Nano))
	w.Header().Set("X-Ratatoskr-Snapshot-Age-Seconds", strconv.FormatFloat(max(0, now.Sub(generatedAt).Seconds()), 'f', 3, 64))
}

// openShiftUpdates retains only complete results for the exact lookup, including
// required release-stream discovery. Advisory text remains optional as in #80.
// Network and enrichment work never mutate a published result or hold mu.
func (s *Service) openShiftUpdates(ctx context.Context, client openshift.Client, req openshift.UpdateRequest) ([]openshift.Release, time.Time, error) {
	s.mu.Lock()
	s.openShiftAttempt++
	attempt, generation := s.openShiftAttempt, s.openShiftGeneration
	s.mu.Unlock()

	values, err := client.Updates(ctx, req)
	if errors.Is(err, openshift.ErrCurrentVersionNotFound) {
		values, err = []openshift.Release{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// A request using the previous configuration cannot publish or fall back
	// into a different upstream's state, even if the URL changes back again.
	if generation != s.openShiftGeneration || client.GraphURL != s.cfg.OpenShiftGraphURL {
		return nil, time.Time{}, errors.New("OpenShift graph configuration changed during lookup")
	}
	previous, found := s.openShiftSnapshots[req]
	if err != nil {
		if found && s.snapshotAcceptableLocked(previous.generatedAt) {
			slog.Warn("serve last-known-good OpenShift releases", "channel", req.Channel, "snapshotAge", s.now().Sub(previous.generatedAt), "error", err)
			return previous.releases, previous.generatedAt, nil
		}
		return nil, time.Time{}, err
	}
	// Concurrent successful requests may finish out of order. Keep the result
	// of the latest started successful lookup; failures never advance its age.
	if found && previous.attempt > attempt {
		if !s.snapshotAcceptableLocked(previous.generatedAt) {
			return nil, time.Time{}, errors.New("OpenShift snapshot has expired")
		}
		return previous.releases, previous.generatedAt, nil
	}
	if !found && len(s.openShiftSnapshots) >= maxOpenShiftSnapshots {
		var oldest openshift.UpdateRequest
		var oldestAttempt uint64
		for key, snapshot := range s.openShiftSnapshots {
			if oldestAttempt == 0 || snapshot.attempt < oldestAttempt {
				oldest, oldestAttempt = key, snapshot.attempt
			}
		}
		delete(s.openShiftSnapshots, oldest)
	}
	generatedAt := s.now().UTC()
	s.openShiftSnapshots[req] = openShiftSnapshot{releases: values, generatedAt: generatedAt, attempt: attempt}
	return values, generatedAt, nil
}
