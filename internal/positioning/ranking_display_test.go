package positioning

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestRankingEvidenceCalendarBoundary(t *testing.T) {
	now := time.Date(2026, time.November, 28, 0, 1, 0, 0, time.FixedZone("user", -8*60*60))
	for _, c := range []struct{ date, want string }{
		{"2026-11-28", "fresh"},
		{"2026-11-14", "fresh"},
		{"2026-11-13", "stale"},
		{"2026-11-29", "unknown"},
		{"", "unknown"},
		{"2026-11-8", "unknown"},
		{"2026-11-27T00:00:00Z", "unknown"},
		{"2026-02-29", "unknown"},
	} {
		d, got := dateFreshness(c.date, now)
		if got != c.want || (got == "unknown" && d != "") || (got != "unknown" && d != c.date) {
			t.Errorf("%q => %q/%q; want %s", c.date, d, got, c.want)
		}
	}
	// On UTC+08 this already looks like Nov 29; the board still uses UTC days.
	now = time.Date(2026, time.November, 29, 2, 0, 0, 0, time.FixedZone("user", 8*60*60))
	if _, got := dateFreshness("2026-11-29", now); got != "unknown" {
		t.Fatalf("future UTC date: %q", got)
	}
}

func TestRankingEvidenceMatchesExactPublishedSnapshot(t *testing.T) {
	home := t.TempDir()
	cfg := []byte(`{"positioning":[{"id":"seed-1","version":12,"model":"seed-only"}]}`)
	if err := os.WriteFile(filepath.Join(home, "config.json"), cfg, 0600); err != nil {
		t.Fatal(err)
	}
	h, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	ctx := context.Background()
	now := time.Date(2026, time.November, 28, 0, 0, 0, 0, time.UTC)
	observe := func() RankingDisplay {
		t.Helper()
		v, err := h.RankingStatus(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	approve := func(rows []model.Positioning) {
		t.Helper()
		p, err := h.Propose(ctx, rows)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.Approve(ctx, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	seed := observe()
	if seed.Published || seed.Version != 0 || seed.EvidenceKey != "" || len(seed.RankedModels) != 0 || !seed.PendingKnown {
		t.Fatalf("unapproved seed counted as an active version: %+v", seed)
	}
	approve([]model.Positioning{{ID: "a", Model: "model-one"}})
	active := observe()
	if !active.Published || active.Version == 0 || len(active.EvidenceKey) != 64 || active.Freshness != "unknown" || len(active.RankedModels) != 1 {
		t.Fatalf("no-evidence active ranking: %+v", active)
	}
	dir := filepath.Join(home, "positioning", "evidence")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fmt.Sprintf("v%d.json", active.Version))
	writeEvidence := func(version int, key, date string) {
		t.Helper()
		body := fmt.Sprintf(`{"version":%d,"evidence_key":%q,"evidence_date":%q}`, version, key, date)
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeEvidence(active.Version, active.EvidenceKey, "2026-11-12")
	if got := observe(); got.EvidenceDate != "2026-11-12" || got.Freshness != "stale" {
		t.Fatalf("verified stale metadata not shown: %+v", got)
	}
	// Even a valid date cannot be shown against a different content hash
	// or in a manifest with a different publication version.
	writeEvidence(active.Version, strings.Repeat("0", 64), "2026-11-28")
	if got := observe(); got.Freshness != "unknown" || got.EvidenceDate != "" {
		t.Fatalf("accepted unrelated hash: %+v", got)
	}
	writeEvidence(active.Version+1, active.EvidenceKey, "2026-11-28")
	if got := observe(); got.Freshness != "unknown" || got.EvidenceDate != "" {
		t.Fatalf("accepted wrong version: %+v", got)
	}
	if err := os.WriteFile(path, []byte(`{"broken"`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := observe(); got.Freshness != "unknown" || got.EvidenceDate != "" {
		t.Fatalf("accepted malformed evidence: %+v", got)
	}
	writeEvidence(active.Version, active.EvidenceKey, "2026-11-21")
	if got := observe(); got.Freshness != "fresh" || got.EvidenceDate != "2026-11-21" {
		t.Fatalf("did not accept exact note: %+v", got)
	}

	approve(nil)
	empty := observe()
	if !empty.Published || empty.Version != active.Version+1 || empty.EvidenceKey == "" || empty.Freshness != "unknown" || len(empty.RankedModels) != 0 {
		t.Fatalf("lost an approved empty version or inherited old evidence: %+v", empty)
	}
}
