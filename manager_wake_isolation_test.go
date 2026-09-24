package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestWakeSubscriptionIsolationYoungSpawnedDoesNotBlockNext(t *testing.T) {
	root := testRoot(t)
	bin, logPath := fakeCodexQueueBin(t, 0)
	threadA := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	threadB := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	mw := &ManagerWakeConfig{
		Enabled:  true,
		CodexBin: bin,
		Subscriptions: []ManagerWakeSubscription{
			{ID: "sub-a", ThreadID: threadA, Projects: []string{"wake-proj"}, Enabled: true},
			{ID: "sub-b", ThreadID: threadB, Projects: []string{"wake-proj"}, Enabled: true},
		},
	}
	tk := heldCommittedTask(t, root, "wake-proj", "isolation-young")
	held := mustHeldEvent(t, root, tk.ID)
	wantID := wakeEventID(tk.ID, held.Seq, held.TransitionID)
	if err := saveManagerWakeInflightPhase(root, "sub-a", threadA, []managerWakeOutboxRow{{WakeEventID: wantID}}, 1, inflightPhaseSpawned); err != nil {
		t.Fatal(err)
	}
	err := managerWakeOnce(root, mw)
	if err == nil {
		t.Fatal("young spawned inflight must keep a non-nil once error")
	}
	if !strings.Contains(err.Error(), "delivery_uncertain") {
		t.Fatalf("error=%v", err)
	}
	cur, class, loadErr := loadManagerWakeCursor(root, "sub-a")
	if loadErr != nil || class != "" || cur == nil || cur.LastErrorClass != "delivery_uncertain" {
		t.Fatalf("sub-a cursor class=%q last=%q err=%v", class, cursorErr(cur), loadErr)
	}
	rec, iclass, ierr := loadManagerWakeInflight(root, "sub-a")
	if ierr != nil || iclass != "" || rec == nil || rec.Phase != inflightPhaseSpawned {
		t.Fatalf("young inflight must stay spawned: %+v class=%q err=%v", rec, iclass, ierr)
	}
	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), threadB) {
		t.Fatalf("subscription B must still be delivered, log=%s", data)
	}
	if strings.Contains(string(data), threadA) {
		t.Fatalf("subscription A must not queue while its inflight is young: %s", data)
	}
}

func TestWakeInflightTTLExpiresOldSpawnedAndRedelivers(t *testing.T) {
	root := testRoot(t)
	bin, logPath := fakeCodexQueueBin(t, 0)
	fixed := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	orig := managerWakeNow
	managerWakeNow = func() time.Time { return fixed }
	t.Cleanup(func() { managerWakeNow = orig })

	threadA := "cccccccc-cccc-cccc-cccc-cccccccccccc"
	mw := &ManagerWakeConfig{
		Enabled:  true,
		CodexBin: bin,
		Subscriptions: []ManagerWakeSubscription{
			{ID: "sub-a", ThreadID: threadA, Projects: []string{"wake-proj"}, Enabled: true},
		},
	}
	tk := heldCommittedTask(t, root, "wake-proj", "isolation-expired")
	held := mustHeldEvent(t, root, tk.ID)
	wantID := wakeEventID(tk.ID, held.Seq, held.TransitionID)
	if err := saveManagerWakeInflightPhase(root, "sub-a", threadA, []managerWakeOutboxRow{{WakeEventID: wantID}}, 1, inflightPhaseSpawned); err != nil {
		t.Fatal(err)
	}
	rec, class, err := loadManagerWakeInflight(root, "sub-a")
	if err != nil || class != "" || rec == nil {
		t.Fatalf("load inflight class=%q err=%v", class, err)
	}
	rec.UpdatedAt = fixed.Add(-31 * time.Minute).Format(time.RFC3339Nano)
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managerWakeInflightPath(root, "sub-a"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	oldErr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	onceErr := managerWakeOnce(root, mw)
	_ = w.Close()
	os.Stderr = oldErr
	if _, err := stderr.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	if onceErr != nil {
		t.Fatalf("expired inflight must not fail once: %v", onceErr)
	}
	if _, err := os.Stat(managerWakeInflightPath(root, "sub-a")); !os.IsNotExist(err) {
		t.Fatalf("live inflight must be moved, stat err=%v", err)
	}
	expiredDir := managerWakeInflightExpiredDir(root)
	info, err := os.Stat(expiredDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("expired dir mode=%o", info.Mode().Perm())
	}
	entries, err := os.ReadDir(expiredDir)
	if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "sub-a-") {
		t.Fatalf("expired entries=%v err=%v", entries, err)
	}
	logText := stderr.String()
	if !strings.Contains(logText, "manager-wake: inflight expired for sub-a (phase=spawned, age=") {
		t.Fatalf("stderr=%q", logText)
	}
	metrics := loadManagerWakeMetrics(root)
	if metrics.InflightExpired != 1 {
		t.Fatalf("inflight_expired=%d", metrics.InflightExpired)
	}
	queued, _ := os.ReadFile(logPath)
	if !strings.Contains(string(queued), threadA) || !strings.Contains(string(queued), wantID) {
		t.Fatalf("expired inflight must redeliver, log=%s", queued)
	}
}

func TestManagerWakeOverlapWarningsSharedProjectIgnoresTaskIDs(t *testing.T) {
	mw := &ManagerWakeConfig{
		Subscriptions: []ManagerWakeSubscription{
			{ID: "alpha", Enabled: true, Projects: []string{"Perlica"}, EventTypes: []string{evDone}},
			{ID: "beta", Enabled: true, Projects: []string{"perlica", "Other"}, EventTypes: []string{evDone, evHeld}},
			{ID: "tasks-only", Enabled: true, TaskIDs: []string{"task-1"}},
			{ID: "disabled", Enabled: false, Projects: []string{"Perlica"}},
			{ID: "elsewhere", Enabled: true, Projects: []string{"Cardex"}, EventTypes: []string{evFailed}},
			{ID: "no-common-event", Enabled: true, Projects: []string{"Perlica"}, EventTypes: []string{evCanceled}},
		},
	}
	warnings := managerWakeOverlapWarnings(mw)
	got := strings.Join(warnings, "\n")
	if !strings.Contains(got, "subscriptions alpha and beta overlap on project Perlica") {
		t.Fatalf("warnings=%q", got)
	}
	if strings.Contains(got, "tasks-only") || strings.Contains(got, "disabled") || strings.Contains(got, "elsewhere") || strings.Contains(got, "no-common-event") {
		t.Fatalf("unexpected overlap: %q", got)
	}
	if len(warnings) != 1 {
		t.Fatalf("want one warning, got %q", got)
	}
}

func cursorErr(cur *managerWakeCursor) string {
	if cur == nil {
		return ""
	}
	return cur.LastErrorClass
}
