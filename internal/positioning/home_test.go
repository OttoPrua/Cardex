package positioning

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cardex/internal/model"
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
	user, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(user, ".cardex")); err == nil {
		t.Fatal("opened ~/.cardex")
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
