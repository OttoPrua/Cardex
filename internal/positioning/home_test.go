package positioning

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	_ "modernc.org/sqlite"
)

func TestCurrentProposeApprove(t *testing.T) {
	home := t.TempDir()
	cfg := []byte(`{
		"ignored": true,
		"positioning": [
			{"id":"p1","model":"opus","family":"anthropic","tiers":["T3"],"capabilities":["web"],"preference":1}
		]
	}`)
	path := filepath.Join(home, "config.json")
	if err := os.WriteFile(path, cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	cur, err := st.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cur) != 1 || cur[0].Model != "opus" || cur[0].Family != "anthropic" || cur[0].Version != 1 || cur[0].Preference != 1 {
		t.Fatalf("seed current %+v", cur)
	}
	prop, err := st.Propose(ctx, []model.Positioning{{
		Model: "sonnet", Family: "anthropic", Tiers: []model.Tier{model.TierT2},
		Capabilities: []string{"web"}, Preference: 2,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if prop.ID == "" || len(prop.Entries) != 1 || prop.Entries[0].Model != "sonnet" {
		t.Fatalf("proposal %+v", prop)
	}
	if err := st.Reject(ctx, prop.ID); err == nil {
		t.Fatal("Reject returned nil")
	}
	still, err := st.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(still) != 1 || still[0].Model != "opus" || still[0].Version != 1 {
		t.Fatalf("propose changed current %+v", still)
	}
	if err := st.Approve(ctx, prop.ID); err != nil {
		t.Fatal(err)
	}
	next, err := st.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].Model != "sonnet" || next[0].Version <= cur[0].Version {
		t.Fatalf("approved %+v previous %d", next, cur[0].Version)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(cfg) {
		t.Fatal("approve rewrote config.json")
	}
}

func TestOpenRefusesCardexHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tasks", "a.json"), []byte(`{"id":"a"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(dir)
	if err == nil || !strings.Contains(err.Error(), "import-legacy") {
		t.Fatalf("Open v1 home: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "cardex.db")); !os.IsNotExist(statErr) {
		t.Fatal("Open created cardex.db in a v1 home")
	}
}

func TestPublishVersionIsMonotonicAcrossOpens(t *testing.T) {
	home := t.TempDir()
	cfg := []byte(`{
		"positioning": [
			{"id":"p1","model":"opus","family":"anthropic","tiers":["T3"],"capabilities":["web"],"preference":1}
		]
	}`)
	if err := os.WriteFile(filepath.Join(home, "config.json"), cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	b, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	ctx := context.Background()

	seed, err := a.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(seed) != 1 || seed[0].Model != "opus" || seed[0].Family != "anthropic" || seed[0].Version != 1 || seed[0].Preference != 1 {
		t.Fatalf("seed %+v", seed)
	}
	fromB, err := b.Current(ctx)
	if err != nil || len(fromB) != 1 || fromB[0].Model != seed[0].Model || fromB[0].Version != seed[0].Version {
		t.Fatalf("second open seed %+v err %v", fromB, err)
	}

	left, err := a.Propose(ctx, []model.Positioning{{
		Model: "left", Family: "anthropic", Tiers: []model.Tier{model.TierT2}, Preference: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	right, err := b.Propose(ctx, []model.Positioning{{
		Model: "right", Family: "openai", Tiers: []model.Tier{model.TierT2}, Preference: 2,
	}})
	if err != nil {
		t.Fatal(err)
	}

	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	publishAfterRead = func() {
		if calls.Add(1) <= 2 {
			arrived <- struct{}{}
			<-release
		}
	}
	t.Cleanup(func() { publishAfterRead = nil })

	var errA, errB error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		errA = a.Approve(ctx, left.ID)
	}()
	go func() {
		defer wg.Done()
		errB = b.Approve(ctx, right.ID)
	}()
	waitArrived := func() {
		t.Helper()
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("approval did not observe the shared version")
		}
	}
	waitArrived()
	waitArrived()
	close(release)
	wg.Wait()
	if errA != nil || errB != nil {
		t.Fatalf("overlap approve a %v b %v", errA, errB)
	}
	got, err := a.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Version != seed[0].Version+2 {
		t.Fatalf("two publishes from %d became %+v", seed[0].Version, got)
	}
	if got[0].Model != "left" && got[0].Model != "right" {
		t.Fatalf("published model %+v", got[0])
	}
	seenByB, err := b.Current(ctx)
	if err != nil || len(seenByB) != 1 || seenByB[0].Version != got[0].Version || seenByB[0].Model != got[0].Model {
		t.Fatalf("second open current %+v err %v", seenByB, err)
	}
	if _, err := os.Stat(filepath.Join(home, "positioning", "current.json")); !os.IsNotExist(err) {
		t.Fatalf("current.json stat %v", err)
	}

	prev := got[0].Version
	for i := 0; i < 2; i++ {
		empty, err := a.Propose(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Approve(ctx, empty.ID); err != nil {
			t.Fatal(err)
		}
		cleared, err := a.Current(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(cleared) != 0 {
			t.Fatalf("empty approval %d restored rows %+v", i, cleared)
		}
	}
	back, err := a.Propose(ctx, []model.Positioning{{
		Model: "opus", Family: "anthropic", Tiers: []model.Tier{model.TierT3}, Capabilities: []string{"web"}, Preference: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Approve(ctx, back.ID); err != nil {
		t.Fatal(err)
	}
	restored, err := a.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || restored[0].Model != "opus" || restored[0].Version != prev+3 {
		t.Fatalf("empty approvals restarted the version: prev %d now %+v", prev, restored)
	}
	after, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(cfg) {
		t.Fatal("approve rewrote config.json")
	}
}

func TestApprovePersistentLockConflict(t *testing.T) {
	home := t.TempDir()
	cfg := []byte(`{"positioning":[{"id":"p1","model":"opus","family":"anthropic","tiers":["T3"],"preference":1}]}`)
	if err := os.WriteFile(filepath.Join(home, "config.json"), cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	before, err := st.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prop, err := st.Propose(ctx, []model.Positioning{{
		Model: "sonnet", Family: "anthropic", Tiers: []model.Tier{model.TierT2}, Preference: 2,
	}})
	if err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(home, "cardex.db")
	side, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=busy_timeout(50)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	side.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = side.Close() })

	var hold atomic.Bool
	hold.Store(true)
	var calls atomic.Int32
	var sideErr atomic.Value
	publishAfterRead = func() {
		if !hold.Load() {
			return
		}
		n := calls.Add(1)
		_, err := side.Exec(`INSERT INTO events (id, ts, entity, event_type, data) VALUES (?, ?, 'lock', 'lock', '{}')`,
			"lock-"+strconv.Itoa(int(n)), time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			sideErr.Store(err)
		}
	}
	t.Cleanup(func() { publishAfterRead = nil })

	err = st.Approve(ctx, prop.ID)
	if v := sideErr.Load(); v != nil {
		t.Fatalf("conflict write: %v", v)
	}
	if calls.Load() < 40 {
		t.Fatalf("lock conflict lasted %d publish attempts, approve err %v", calls.Load(), err)
	}
	if err == nil || !publishConflict(err) {
		t.Fatalf("approve err %v", err)
	}
	saved, err := st.readProposal(prop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status == "approved" {
		t.Fatalf("proposal marked %s after a failed publish", saved.Status)
	}
	cur, err := st.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cur) != 1 || cur[0].Model != before[0].Model || cur[0].Version != before[0].Version {
		t.Fatalf("current changed from %+v to %+v", before, cur)
	}

	hold.Store(false)
	if err := st.Approve(ctx, prop.ID); err != nil {
		t.Fatal(err)
	}
	next, err := st.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].Model != "sonnet" || next[0].Version <= before[0].Version {
		t.Fatalf("retried approve %+v previous %+v", next, before)
	}
	saved, err = st.readProposal(prop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != "approved" {
		t.Fatalf("status %s", saved.Status)
	}
}

func TestProposalDiffIncludesTierAndCapability(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		cfg   string
		next  model.Positioning
		want  []string
		empty bool
	}{
		{
			name: "tier only",
			cfg:  `{"positioning":[{"model":"opus","family":"anthropic","tiers":["T2"],"capabilities":[],"preference":1}]}`,
			next: model.Positioning{Model: "opus", Family: "anthropic", Tiers: []model.Tier{model.TierT3}, Capabilities: []string{}, Preference: 1},
			want: []string{"opus", "tier", "T2", "T3"},
		},
		{
			name: "add web",
			cfg:  `{"positioning":[{"model":"opus","family":"anthropic","tiers":["T2"],"preference":1}]}`,
			next: model.Positioning{Model: "opus", Family: "anthropic", Tiers: []model.Tier{model.TierT2}, Capabilities: []string{"web"}, Preference: 1},
			want: []string{"opus", "capability", "web"},
		},
		{
			name: "remove web",
			cfg:  `{"positioning":[{"model":"opus","family":"anthropic","tiers":["T2"],"capabilities":["web"],"preference":1}]}`,
			next: model.Positioning{Model: "opus", Family: "anthropic", Tiers: []model.Tier{model.TierT2}, Capabilities: []string{}, Preference: 1},
			want: []string{"opus", "capability", "web"},
		},
		{
			name: "family only",
			cfg:  `{"positioning":[{"model":"opus","family":"anthropic","tiers":["T2"],"preference":1}]}`,
			next: model.Positioning{Model: "opus", Family: "openai", Tiers: []model.Tier{model.TierT2}, Preference: 1},
			want: []string{"opus", "family"},
		},
		{
			name: "preference only",
			cfg:  `{"positioning":[{"model":"opus","family":"anthropic","tiers":["T2"],"preference":1}]}`,
			next: model.Positioning{Model: "opus", Family: "anthropic", Tiers: []model.Tier{model.TierT2}, Preference: 4},
			want: []string{"opus", "preference"},
		},
		{
			name:  "unchanged",
			cfg:   `{"positioning":[{"model":"opus","family":"anthropic","tiers":["T2"],"capabilities":["web"],"preference":1}]}`,
			next:  model.Positioning{Model: "opus", Family: "anthropic", Tiers: []model.Tier{model.TierT2}, Capabilities: []string{"web"}, Preference: 1},
			empty: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := openPositioning(t, tc.cfg)
			prop, err := st.Propose(ctx, []model.Positioning{tc.next})
			if err != nil {
				t.Fatal(err)
			}
			if tc.empty {
				if prop.Diff != "" {
					t.Fatalf("diff %q", prop.Diff)
				}
				return
			}
			if prop.Diff == "" {
				t.Fatal("empty diff")
			}
			for _, part := range tc.want {
				if !strings.Contains(prop.Diff, part) {
					t.Fatalf("diff %q missing %q", prop.Diff, part)
				}
			}
		})
	}
}

func TestApproveNormalizesTierAndMarksUnknown(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		tiers  []model.Tier
		want   []model.Tier
		review bool
	}{
		{name: "t2", tiers: []model.Tier{"t2"}, want: []model.Tier{model.TierT2}},
		{name: "spaced t2", tiers: []model.Tier{" t2 "}, want: []model.Tier{model.TierT2}},
		{name: "spaced T3", tiers: []model.Tier{" T3 "}, want: []model.Tier{model.TierT3}},
		{name: "unknown", tiers: []model.Tier{"not-a-tier"}, want: []model.Tier{}, review: true},
		{name: "t2 and unknown", tiers: []model.Tier{"t2", "not-a-tier"}, want: []model.Tier{model.TierT2}, review: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := openPositioning(t, `{"positioning":[]}`)
			prop, err := st.Propose(ctx, []model.Positioning{{
				Model: "opus", Family: "anthropic", Tiers: tc.tiers, Preference: 1,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Approve(ctx, prop.ID); err != nil {
				t.Fatalf("irregular tier aborted approve: %v", err)
			}
			saved, err := st.readProposal(prop.ID)
			if err != nil {
				t.Fatal(err)
			}
			cur, err := st.Current(ctx)
			if err != nil || len(cur) != 1 || !sameTiers(cur[0].Tiers, tc.want) {
				t.Fatalf("published %+v want %v err %v", cur, tc.want, err)
			}
			for _, tier := range cur[0].Tiers {
				raw := string(tier)
				if raw == "t2" || raw == " T3 " || raw == "not-a-tier" || raw != strings.ToUpper(strings.TrimSpace(raw)) {
					t.Fatalf("raw tier published %s", tier)
				}
			}
			if tc.review {
				if saved.Status != "待复核" || !strings.Contains(saved.Diff, "待复核") {
					t.Fatalf("proposal not marked %+v", saved)
				}
				t.Log("待复核 without publishing the unknown token")
				return
			}
			if saved.Status != "approved" {
				t.Fatalf("status %s", saved.Status)
			}
			t.Logf("success canonical %s", joinTiers(tc.want))
		})
	}
}

func TestApprovedEmptyVersionSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	const seedModel = "seed-opus"
	cfg := `{"positioning":[{"id":"seed","model":"seed-opus","family":"anthropic","tiers":["T3"],"preference":1}]}`
	cases := []struct {
		name  string
		plant func(t *testing.T, home string)
	}{
		{name: "positioning_meta", plant: plantPositioningMetaVersion},
		{name: "version carrier", plant: plantVersionCarrier},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			st, err := Open(home)
			if err != nil {
				t.Fatal(err)
			}
			tc.plant(t, home)
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			st, err = Open(home)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })

			cur, err := st.Current(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range cur {
				if row.Model == seedModel || row.ID == "seed" {
					t.Fatalf("Current substituted the seed: %+v", cur)
				}
			}
			t.Logf("Current is not the seed: %+v", cur)

			same, err := st.Propose(ctx, []model.Positioning{{
				Model: seedModel, Family: "anthropic", Tiers: []model.Tier{model.TierT3}, Preference: 1,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if same.Diff == "" || strings.Contains(same.Diff, "- "+seedModel) {
				t.Fatalf("Propose is based on the seed: %q", same.Diff)
			}
			if !strings.Contains(same.Diff, "+ "+seedModel) {
				t.Fatalf("Propose diff %q", same.Diff)
			}
			t.Logf("Propose is not based on the seed: %q", same.Diff)

			next, err := st.Propose(ctx, []model.Positioning{{
				Model: "opus", Family: "anthropic", Tiers: []model.Tier{model.TierT2}, Preference: 1,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Approve(ctx, next.ID); err != nil {
				t.Fatal(err)
			}
			got, err := st.Current(ctx)
			if err != nil || len(got) != 1 || got[0].Model != "opus" || got[0].Version <= 7 {
				t.Fatalf("published version %+v err %v", got, err)
			}
			t.Logf("published version %d is greater than 7", got[0].Version)
		})
	}
}

func plantPositioningMetaVersion(t *testing.T, home string) {
	t.Helper()
	db := openHomeDB(t, home)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS positioning_meta (id TEXT PRIMARY KEY, version INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO positioning_meta(id, version) VALUES ('version', 7)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM positioning`); err != nil {
		t.Fatal(err)
	}
}

func plantVersionCarrier(t *testing.T, home string) {
	t.Helper()
	db := openHomeDB(t, home)
	defer db.Close()
	if _, err := db.Exec(`DELETE FROM positioning`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO positioning(id, version, model, family, tiers, capabilities, preference) VALUES (?, 7, '', '', '[]', '[]', 0)`, versionRowID); err != nil {
		t.Fatal(err)
	}
}

func openHomeDB(t *testing.T, home string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(home, "cardex.db"))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestEmptyPublishHidesVersionRow(t *testing.T) {
	ctx := context.Background()
	st := openPositioning(t, `{"positioning":[{"model":"opus","family":"anthropic","tiers":["T3"],"preference":1}]}`)
	seed, err := st.Current(ctx)
	if err != nil || len(seed) != 1 || seed[0].Model != "opus" {
		t.Fatalf("seed %+v err %v", seed, err)
	}
	empty, err := st.Propose(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Approve(ctx, empty.ID); err != nil {
		t.Fatal(err)
	}
	cleared, err := st.Current(ctx)
	if err != nil || len(cleared) != 0 {
		t.Fatalf("empty current %+v err %v", cleared, err)
	}
	nextProp, err := st.Propose(ctx, []model.Positioning{{
		Model: "opus", Family: "anthropic", Tiers: []model.Tier{model.TierT2}, Preference: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Approve(ctx, nextProp.ID); err != nil {
		t.Fatal(err)
	}
	next, err := st.Current(ctx)
	if err != nil || len(next) != 1 || next[0].Model != "opus" || next[0].ID == versionRowID || next[0].Version <= seed[0].Version+1 {
		t.Fatalf("later version %+v seed %d err %v", next, seed[0].Version, err)
	}
}

func openPositioning(t *testing.T, cfg string) *Home {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestCurrentAndProposeReadOneSnapshot(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		publish bool
		entry   string
	}{
		{name: "current old rows", publish: true, entry: "current"},
		{name: "propose old rows", publish: true, entry: "propose"},
		{name: "current first approve", publish: false, entry: "current"},
		{name: "propose first approve", publish: false, entry: "propose"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			cfg := []byte(`{"positioning":[{"id":"p1","model":"opus","family":"anthropic","tiers":["T3"],"capabilities":["web"],"preference":1}]}`)
			if err := os.WriteFile(filepath.Join(home, "config.json"), cfg, 0o644); err != nil {
				t.Fatal(err)
			}
			a, err := Open(home)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = a.Close() })
			b, err := Open(home)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = b.Close() })

			if tc.publish {
				first, err := a.Propose(ctx, []model.Positioning{{
					ID: "old", Model: "opus", Family: "anthropic", Tiers: []model.Tier{model.TierT3},
					Capabilities: []string{"web"}, Preference: 1,
				}})
				if err != nil {
					t.Fatal(err)
				}
				if err := a.Approve(ctx, first.ID); err != nil {
					t.Fatal(err)
				}
			}
			before, err := a.Current(ctx)
			if err != nil {
				t.Fatal(err)
			}
			gap, err := b.Propose(ctx, []model.Positioning{{
				ID: "new", Model: "sonnet", Family: "openai", Tiers: []model.Tier{model.TierT2}, Preference: 2,
			}})
			if err != nil {
				t.Fatal(err)
			}

			var approveErr error
			currentAfterContent = func() {
				approveErr = b.Approve(ctx, gap.ID)
			}
			t.Cleanup(func() { currentAfterContent = nil })

			probe := []model.Positioning{{
				ID: "probe", Model: "probe", Family: "probe", Tiers: []model.Tier{model.TierT1}, Preference: 1,
			}}
			var got []model.Positioning
			var prop Proposal
			if tc.entry == "current" {
				got, err = a.Current(ctx)
			} else {
				prop, err = a.Propose(ctx, probe)
			}
			if err != nil {
				t.Fatal(err)
			}
			if approveErr != nil {
				t.Fatalf("overlap approve: %v", approveErr)
			}
			currentAfterContent = nil

			after, err := a.Current(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != 1 || after[0].Model != "sonnet" || samePositioningSnapshot(before, after) {
				t.Fatalf("approved snapshot %+v before %+v", after, before)
			}
			if tc.entry == "current" {
				if !samePositioningSnapshot(got, before) && !samePositioningSnapshot(got, after) {
					t.Fatalf("current %+v matches neither before %+v nor after %+v", got, before, after)
				}
				return
			}
			if prop.Diff != summarize(before, probe) && prop.Diff != summarize(after, probe) {
				t.Fatalf("diff %q matches neither snapshot", prop.Diff)
			}
		})
	}
}
