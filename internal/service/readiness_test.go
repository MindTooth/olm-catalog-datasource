package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/MindTooth/ratatoskr/pkg/catalog"
)

func TestReadinessRequiresEveryConfiguredSource(t *testing.T) {
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	first := catalog.Source{ID: "first", Image: "registry.example/first"}
	second := catalog.Source{ID: "second", Image: "registry.example/second"}
	svc := New(Config{Sources: []catalog.Source{first, second}, MaxSnapshotAge: time.Hour})
	svc.now = func() time.Time { return now }
	svc.readCatalog = func(ctx context.Context, source catalog.Source, reader catalog.Reader) (*catalog.Snapshot, error) {
		return reader.ReadFS(ctx, source, catalogFixture("1.1.0"))
	}
	check := func(want int) {
		t.Helper()
		if res := getSnapshotResponse(svc, "/readyz"); res.Code != want {
			t.Fatalf("readiness = %d, want %d: %s", res.Code, want, res.Body.String())
		}
		if res := getSnapshotResponse(svc, "/healthz"); res.Code != http.StatusOK {
			t.Fatal("catalog availability must not affect process health")
		}
	}
	check(http.StatusServiceUnavailable)
	if err := svc.Refresh(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	check(http.StatusServiceUnavailable) // One catalog cannot admit a partially initialized replica.
	now = now.Add(30 * time.Minute)
	if err := svc.Refresh(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	check(http.StatusOK)
	now = now.Add(30 * time.Minute)
	check(http.StatusOK) // The oldest snapshot is acceptable at the exact limit.
	now = now.Add(time.Nanosecond)
	check(http.StatusServiceUnavailable) // A fresh second catalog cannot mask an expired first.
	if err := svc.Refresh(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	check(http.StatusOK)
	// A removed or reconfigured source must not satisfy the new configuration.
	svc.cfg.Sources[0].Image = "registry.example/replacement"
	check(http.StatusServiceUnavailable)
	svc.cfg.Sources = nil
	check(http.StatusServiceUnavailable)
}

func TestIndependentReplicasRetainCompleteSnapshots(t *testing.T) {
	source := catalog.Source{ID: "test", Image: "registry.example/catalog"}
	cfg := Config{Sources: []catalog.Source{source}, MaxSnapshotAge: time.Hour}
	first, second := New(cfg), New(cfg)
	configs := catalogFixture("1.1.0")
	read := func(ctx context.Context, source catalog.Source, reader catalog.Reader) (*catalog.Snapshot, error) {
		return reader.ReadFS(ctx, source, configs)
	}
	first.readCatalog, second.readCatalog = read, read
	for _, replica := range []*Service{first, second} {
		if err := replica.Refresh(context.Background(), source); err != nil {
			t.Fatal(err)
		}
	}
	configs = catalogFixture("1.2.0")
	if err := first.Refresh(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	// The second replica sees a failed partial refresh and keeps its complete result.
	configs["catalog.json"].Data = append(configs["catalog.json"].Data, []byte("\n{")...)
	if err := second.Refresh(context.Background(), source); err == nil {
		t.Fatal("partial refresh should fail")
	}
	route := "/v2/sources/test/packages/example/updates?currentVersion=1.0.0"
	for i, replica := range []*Service{first, second} {
		want := []string{"1.2.0", "1.1.0"}[i]
		res := getSnapshotResponse(replica, route)
		var body struct {
			Releases []struct{ Version string }
		}
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if res.Code != http.StatusOK || len(body.Releases) != 2 || body.Releases[0].Version != "1.0.0" || body.Releases[1].Version != want {
			t.Fatalf("replica %d did not return its complete snapshot: %d %s", i, res.Code, res.Body.String())
		}
		if res := getSnapshotResponse(replica, "/readyz"); res.Code != http.StatusOK {
			t.Fatalf("replica %d with valid LKG is not ready", i)
		}
	}
	// A replacement has no retained data; the survivor remains independently available.
	restarted := New(cfg)
	requireUnavailable(t, getSnapshotResponse(restarted, "/readyz"))
	requireUnavailable(t, getSnapshotResponse(restarted, route))
	if res := getSnapshotResponse(second, route); res.Code != http.StatusOK {
		t.Fatal("peer restart affected the surviving replica")
	}
}
