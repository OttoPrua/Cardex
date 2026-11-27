package board

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/positioning"
)

func TestBoardRankingSeparatesPublishedAndPending(t *testing.T) {
	db, home := seedCurrent(t)
	table := positioning.Inspect(home, db)
	ctx := context.Background()
	ch, err := db.GetChannel(ctx, "ch-main")
	must(t, err)
	ch.Models = []string{"already-ranked", "pending-model", "pending-model"}
	must(t, db.UpdateChannel(ctx, &ch))
	approved, err := table.Propose(ctx, []model.Positioning{{ID: "published", Model: "already-ranked"}})
	must(t, err)
	must(t, table.Approve(ctx, approved.ID))
	pending, err := table.Propose(ctx, []model.Positioning{{ID: "pending", Model: "pending-model"}})
	must(t, err)
	rows, err := db.ListPositioning(ctx)
	must(t, err)
	before := snapshot(t, db)
	pendingPath := filepath.Join(home, "positioning", "proposals", pending.ID+".json")
	proposal, err := os.ReadFile(pendingPath)
	must(t, err)
	page := do(t, http.MethodGet, "/channels?tab=ranking")
	for _, want := range []string{">生效排名<", ">待批提案<", ">证据日期<", ">未知<", ">未入榜的已配置模型<", pending.ID, "pending-model"} {
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), want) {
			t.Fatalf("ranking HTML missing %q (HTTP %d)", want, page.Code)
		}
	}
	if strings.Contains(page.Body.String(), approved.ID) {
		t.Fatal("approved proposal is shown as pending")
	}
	js := do(t, http.MethodGet, "/api/channels?tab=ranking")
	var got struct {
		Tab     string
		Ranking struct {
			Version       int
			Freshness     string
			Pending       []struct{ ID string }
			MissingModels []string `json:"missing_models"`
		}
	}
	must(t, json.Unmarshal(js.Body.Bytes(), &got))
	if js.Code != http.StatusOK || got.Tab != "ranking" || got.Ranking.Version != 1 || got.Ranking.Freshness != "unknown" ||
		len(got.Ranking.Pending) != 1 || got.Ranking.Pending[0].ID != pending.ID ||
		!reflect.DeepEqual(got.Ranking.MissingModels, []string{"pending-model"}) {
		t.Fatalf("unexpected ranking projection: %+v", got)
	}
	rowsAfter, err := db.ListPositioning(ctx)
	must(t, err)
	newProposal, err := os.ReadFile(pendingPath)
	must(t, err)
	if !reflect.DeepEqual(before, snapshot(t, db)) || !reflect.DeepEqual(rows, rowsAfter) || string(newProposal) != string(proposal) {
		t.Fatal("ranking page changed database or proposal")
	}
	if _, err := os.Stat(filepath.Join(home, "positioning", "evidence")); !os.IsNotExist(err) {
		t.Fatalf("ranking page generated evidence directory: %v", err)
	}
}

func TestRankingTabDisplaysStaleEvidenceAndIncompleteProposals(t *testing.T) {
	db, home := seedCurrent(t)
	table := positioning.Inspect(home, db)
	ctx := context.Background()
	approved, err := table.Propose(ctx, []model.Positioning{{ID: "published", Model: "model-one"}})
	must(t, err)
	must(t, table.Approve(ctx, approved.ID))
	now := time.Date(2026, time.November, 28, 0, 0, 0, 0, time.UTC)
	state, err := table.RankingStatus(ctx, now)
	must(t, err)
	dir := filepath.Join(home, "positioning", "evidence")
	must(t, os.MkdirAll(dir, 0o700))
	evidence := filepath.Join(dir, fmt.Sprintf("v%d.json", state.Version))
	must(t, os.WriteFile(evidence, []byte(fmt.Sprintf(`{"version":%d,"evidence_key":%q,"evidence_date":"2026-11-13"}`, state.Version, state.EvidenceKey)), 0o600))
	proposal := filepath.Join(home, "positioning", "proposals", "0123456789abcdef.json")
	must(t, os.WriteFile(proposal, []byte("{malformed-secret-sentinel"), 0o600))
	view := loadRanking(ctx, home, db, []channelView{{Models: []string{"model-one"}}}, now)
	var page strings.Builder
	must(t, pageTmpl.ExecuteTemplate(&page, "ranking", view))
	for _, part := range []string{`<time datetime="2026-11-13">2026-11-13</time>`, "证据已超过两周，建议复核", "待批列表不完整或不可读"} {
		if !strings.Contains(page.String(), part) {
			t.Fatalf("ranking lacks %q", part)
		}
	}
	if strings.Contains(page.String(), "secret-sentinel") || view.PendingKnown || len(view.MissingModels) != 0 {
		t.Fatal("ranking misrepresented or leaked incomplete evidence")
	}
}

func TestRankingTabNeverInitializesMissingDatabase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CARDEX_HOME", home)
	CloseDB()
	t.Cleanup(CloseDB)
	rec := do(t, http.MethodGet, "/api/channels?tab=ranking")
	if rec.Code != http.StatusOK {
		t.Fatalf("ranking unavailable: %d", rec.Code)
	}
	files, err := os.ReadDir(home)
	must(t, err)
	if len(files) != 0 {
		t.Fatalf("ranking GET created files: %v", files)
	}
	var got struct {
		Ranking struct {
			PublishedKnown bool   `json:"published_known"`
			Freshness      string `json:"freshness"`
		}
	}
	must(t, json.Unmarshal(rec.Body.Bytes(), &got))
	if got.Ranking.PublishedKnown || got.Ranking.Freshness != "unknown" {
		t.Fatalf("missing DB misreported: %+v", got)
	}
}
