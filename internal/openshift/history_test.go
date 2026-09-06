package openshift

import (
	"strings"
	"testing"
)

func TestParseReleaseStreamIncludesAcceptedGAReleasesOnly(t *testing.T) {
	const page = `<table><tbody>
<tr><td><a href="/releasestream/4-stable/release/4.22.12">4.22.12</a></td><td>Accepted</td></tr>
<tr><td><a href="/releasestream/4-stable/release/4.22.11">4.22.11</a></td><td>Accepted*</td></tr>
<tr><td><a href="/releasestream/4-stable/release/4.22.10">4.22.10</a></td><td>Accepted</td></tr>
<tr><td><a href="/releasestream/4-stable/release/4.22.9">4.22.9</a></td><td>Rejected*</td></tr>
<tr><td><a href="/releasestream/4-stable/release/4.22.0-rc.5">4.22.0-rc.5</a></td><td>Accepted</td></tr>
</tbody></table>`
	entries, err := parseReleaseStream(strings.NewReader(page), "https://amd64.ocp.releases.ci.openshift.org/releasestream/4-stable")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %#v", entries)
	}
	for i, want := range []string{"4.22.10", "4.22.11", "4.22.12"} {
		if entries[i].version != want {
			t.Fatalf("entries[%d].version = %q, want %q", i, entries[i].version, want)
		}
	}
}

func TestReleasesBetweenUsesReleaseStreamNotGraphEdges(t *testing.T) {
	entries := []releaseHistoryEntry{
		{version: "4.22.9"},
		{version: "4.22.10"},
		{version: "4.22.11"},
		{version: "4.22.12"},
	}
	history := releasesBetween(entries, "4.22.9", "4.22.12")
	if len(history) != 3 {
		t.Fatalf("history = %#v", history)
	}
	for i, want := range []string{"4.22.10", "4.22.11", "4.22.12"} {
		if history[i].version != want {
			t.Fatalf("history[%d] = %q, want %q", i, history[i].version, want)
		}
	}
}

func TestReleasesBetweenCrossesMinorVersion(t *testing.T) {
	entries := []releaseHistoryEntry{
		{version: "4.21.18"},
		{version: "4.21.19"},
		{version: "4.21.20"},
		{version: "4.22.0"},
		{version: "4.22.1"},
	}
	history := releasesBetween(entries, "4.21.18", "4.22.1")
	if len(history) != 4 || history[0].version != "4.21.19" || history[2].version != "4.22.0" || history[3].version != "4.22.1" {
		t.Fatalf("history = %#v", history)
	}
}
