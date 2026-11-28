package loop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

func changeComparisonIntegration(t *testing.T, repo, path, body string) string {
	t.Helper()
	gitRun(t, repo, "checkout", "integration")
	if body == "" {
		if err := os.Remove(filepath.Join(repo, path)); err != nil {
			t.Fatal(err)
		}
	} else if err := os.WriteFile(filepath.Join(repo, path), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "--", path)
	gitRun(t, repo, "commit", "-m", "change synthetic candidate")
	oid := gitSHA(t, repo, "integration")
	gitRun(t, repo, "checkout", "--detach", "main")
	return oid
}

func pendingComparisonApproval(t *testing.T, f *fx, oid, previous string) model.Question {
	t.Helper()
	var q model.Question
	for _, q0 := range questionsKind(f, model.QuestionDone) {
		if q0.Answer == "" && q0.ID != previous && strings.Contains(q0.Text, oid) {
			if q.ID != "" {
				t.Fatal("more than one exact pending approval for the test candidate")
			}
			q = q0
		}
	}
	if q.ID == "" {
		t.Fatal("missing fresh exact candidate approval")
	}
	return q
}

func approveComparisonFixture(t *testing.T, f *fx, q model.Question) {
	t.Helper()
	q.Answer = "approve"
	if err := f.db.UpdateQuestion(f.ctx, &q); err != nil {
		t.Fatal(err)
	}
}

func TestAPI2ChangedIntegrationAcceptanceNeverReusesApprovedQuestion(t *testing.T) {
	for _, mode := range []string{"late-content-fails-and-reset-still-needs-fresh-approval", "late-content-passes-and-needs-fresh-approval",
		"changed-acceptance-at-same-tip-needs-fresh-approval"} {
		t.Run(mode, func(t *testing.T) {
			f, repo, main := api2CompletionFixture(t)
			c := api2StandaloneCard(f, nil)
			api2HoldUnlaunchedCard(f, c)
			f.step()
			original := gitSHA(t, repo, "integration")
			old := pendingComparisonApproval(t, f, original, "")
			approveComparisonFixture(t, f, old)
			var target string
			switch mode {
			case "late-content-fails-and-reset-still-needs-fresh-approval":
				target = changeComparisonIntegration(t, repo, "marker", "")
			case "late-content-passes-and-needs-fresh-approval":
				target = changeComparisonIntegration(t, repo, "new-local-content", "later fixture\n")
			default:
				target = original
				f.setCommands([]string{"test -f marker", "true"})
			}
			f.step()
			if len(f.events(comparisonApprovalInvalidated)) != 1 || len(questionsKind(f, model.QuestionDone)) != 1 ||
				gitSHA(t, repo, "main") != main {
				t.Fatal("old answer was not durably invalidated before any fresh qualification")
			}
			f.step()
			if mode == "late-content-fails-and-reset-still-needs-fresh-approval" {
				if len(f.events(comparisonAcceptanceRejected)) != 1 || len(questionsKind(f, model.QuestionDone)) != 1 ||
					f.objective().Status != model.ObjectiveDoneCandidate || gitSHA(t, repo, "main") != main {
					t.Fatal("moved failing candidate got new approval or merged")
				}
				f.step()
				if len(f.events(comparisonAcceptanceRejected)) != 1 {
					t.Fatal("identical failed candidate caused repeat acceptance side effects")
				}
				gitRun(t, repo, "update-ref", "refs/heads/integration", original, target)
				target = original
				f.step()
			}
			newQ := pendingComparisonApproval(t, f, target, old.ID)
			if len(questionsKind(f, model.QuestionDone)) != 2 || gitSHA(t, repo, "main") != main {
				t.Fatal("new exact question did not preserve main")
			}
			f.step()
			if gitSHA(t, repo, "main") != main {
				t.Fatal("old approved question authorized the refreshed candidate")
			}
			approveComparisonFixture(t, f, newQ)
			f.step()
			if f.objective().Status != model.ObjectiveDone || gitSHA(t, repo, "main") != target ||
				len(f.events("merged_local")) != 1 || f.mustCard(c.ID).Status != model.CardNeedsInput {
				t.Fatal("fresh exact approval did not advance exactly the tested commit")
			}
		})
	}
}

func TestAPI2OptionalPrimaryEligibilityHoldDoesNotReplaceApproval(t *testing.T) {
	f, repo, main := api2CompletionFixture(t)
	c := api2StandaloneCard(f, nil)
	api2HoldUnlaunchedCard(f, c)
	f.step()
	target := gitSHA(t, repo, "integration")
	q := pendingComparisonApproval(t, f, target, "")
	approveComparisonFixture(t, f, q)
	gitRun(t, repo, "checkout", "main")
	f.step()
	f.step()
	holds := f.events(comparisonGitHeld)
	var data map[string]string
	if len(holds) != 1 || json.Unmarshal(holds[0].Data, &data) != nil || data["reason"] != "primary_checked_out" ||
		len(questionsKind(f, model.QuestionDone)) != 1 || len(f.events(comparisonApprovalInvalidated)) != 0 ||
		gitSHA(t, repo, "main") != main || f.objective().Status != model.ObjectiveDoneCandidate {
		t.Fatal("primary eligibility failure was hidden, changed main, or reopened acceptance")
	}
	gitRun(t, repo, "checkout", "--detach", "main")
	f.step()
	if f.objective().Status != model.ObjectiveDone || gitSHA(t, repo, "main") != target || len(questionsKind(f, model.QuestionDone)) != 1 {
		t.Fatal("restored primary eligibility required a redundant approval or failed to advance")
	}
}

func TestAPI2DurableUnknownGitOutcomePreventsRetryAndReapproval(t *testing.T) {
	f, repo, main := api2CompletionFixture(t)
	c := api2StandaloneCard(f, nil)
	api2HoldUnlaunchedCard(f, c)
	f.step()
	old := gitSHA(t, repo, "integration")
	q := pendingComparisonApproval(t, f, old, "")
	approveComparisonFixture(t, f, q)
	raw, err := json.Marshal(map[string]string{"question_id": q.ID, "accepted_commit": old, "reason": "operator_reconciliation_required"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.AppendEvent(f.ctx, &model.Event{Entity: "objective:" + f.obj.ID, Type: comparisonGitUnknown, Data: raw}); err != nil {
		t.Fatal(err)
	}
	changeComparisonIntegration(t, repo, "later-pass", "fixture\n")
	f.step()
	f.step()
	if gitSHA(t, repo, "main") != main || f.objective().Status != model.ObjectiveDoneCandidate ||
		len(questionsKind(f, model.QuestionDone)) != 1 || len(f.events(comparisonApprovalInvalidated)) != 0 ||
		len(f.events("merged_local")) != 0 {
		t.Fatal("a persisted uncertain Git outcome was automatically retried or reapproved")
	}
	assertAPI2OrdinaryResumeDoesNotReconcile(t, f, repo, main)
}

func TestAPI2RequalificationReservesGraphWriterThroughExactAcceptance(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a temporary Unix acceptance command")
	}
	f, repo, main := api2CompletionFixture(t)
	c := api2StandaloneCard(f, nil)
	api2HoldUnlaunchedCard(f, c)
	dependent := api2CompletionDependency(f, c, true, false)
	dependent.Deps = nil
	if err := f.db.UpdateCard(f.ctx, &dependent); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ready, resume := filepath.Join(dir, "acceptance-ready"), filepath.Join(dir, "resume")
	script := `test -f marker || exit 1
if [ -f changed-acceptance ]; then
	printf 'ready\n' > '` + ready + `' || exit 1
	i=0
	while [ ! -f '` + resume + `' ] && [ "$i" -lt 200 ]; do
		i=$((i + 1))
		sleep 0.05
	done
	test -f '` + resume + `' || exit 1
fi`
	f.setCommands([]string{script})
	f.step()
	original := gitSHA(t, repo, "integration")
	q := pendingComparisonApproval(t, f, original, "")
	approveComparisonFixture(t, f, q)
	newOID := changeComparisonIntegration(t, repo, "changed-acceptance", "fixture\n")
	f.step() // Persist old approval invalidation first.
	release := func() { _ = os.WriteFile(resume, []byte("release"), 0o600) }
	defer release()
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- DevStep(ctx, f.obj.ID) }()
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("did not reach acceptance at the new candidate commit: %v", err)
		case <-ctx.Done():
			t.Fatal("timed out waiting for synthetic acceptance")
		case <-time.After(10 * time.Millisecond):
		}
	}
	raw, err := json.Marshal([]string{c.ID})
	if err != nil {
		t.Fatal(err)
	}
	db := api2OpenFixtureDatabase(t, f.home)
	_, err = db.ExecContext(ctx, "UPDATE cards SET deps=? WHERE id=?", string(raw), dependent.ID)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "locked") {
		t.Fatalf("concurrent graph writer bypassed the requalification reservation: %v", err)
	}
	release()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if pending := pendingComparisonApproval(t, f, newOID, q.ID); pending.Answer != "" || gitSHA(t, repo, "main") != main {
		t.Fatal("exact acceptance itself merged or required no fresh answer")
	}
}

func TestAPI2ExactAcceptanceDoesNotUseGitReplacementContent(t *testing.T) {
	f, repo, main := api2CompletionFixture(t)
	c := api2StandaloneCard(f, nil)
	api2HoldUnlaunchedCard(f, c)
	realPass := gitSHA(t, repo, "integration")
	realFail := changeComparisonIntegration(t, repo, "marker", "")
	f.setCommands([]string{`test "$GIT_NO_REPLACE_OBJECTS" = 1 && test -f marker`})
	gitRun(t, repo, "replace", realFail, realPass)
	if got := gitOut(t, repo, "show", realFail+":marker"); !strings.Contains(got, "ok") {
		t.Fatal("fixture lacks a real passing Git replacement")
	}
	f.step()
	if f.objective().Status != model.ObjectiveActive || len(questionsKind(f, model.QuestionDone)) != 0 ||
		gitSHA(t, repo, "main") != main || len(f.events(comparisonGitEntryEvent)) != 0 {
		t.Fatal("a replacement provided authority for the original failing commit")
	}
	gitRun(t, repo, "replace", "-d", realFail)
	gitRun(t, repo, "update-ref", "refs/heads/integration", realPass, realFail)
	f.step()
	if q := pendingComparisonApproval(t, f, realPass, ""); q.Answer != "" || gitSHA(t, repo, "main") != main {
		t.Fatal("real original passing candidate lacked a fresh approval or mutated the primary")
	}
}

func TestAPI2ExactAcceptanceIgnoresMutableSourceAdminAttributes(t *testing.T) {
	f, repo, main := api2CompletionFixture(t)
	c := api2StandaloneCard(f, nil)
	api2HoldUnlaunchedCard(f, c)
	target := gitSHA(t, repo, "integration")
	attrs := filepath.Join(repo, ".git", "info", "attributes")
	if err := os.MkdirAll(filepath.Dir(attrs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(attrs, []byte("marker text eol=crlf\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, repo, "check-attr", "eol", "--", "marker"); !strings.Contains(got, "crlf") {
		t.Fatal("fixture lacks effective mutable source attributes")
	}
	f.setCommands([]string{`test "$(cat marker)" = ok && test ! -e .git/info/attributes`})
	f.step()
	q := pendingComparisonApproval(t, f, target, "")
	if q.Answer != "" || gitSHA(t, repo, "main") != main || len(f.events(comparisonGitEntryEvent)) != 0 {
		t.Fatal("synthetic owned materialization touched primary or reused approval")
	}
}

func TestAPI2OptionalBusyRepositoryDoesNotRevokeCurrentApproval(t *testing.T) {
	f, repo, main := api2CompletionFixture(t)
	c := api2StandaloneCard(f, nil)
	api2HoldUnlaunchedCard(f, c)
	f.step()
	target := gitSHA(t, repo, "integration")
	q := pendingComparisonApproval(t, f, target, "")
	approveComparisonFixture(t, f, q)
	busy := filepath.Join(repo, ".git", "MERGE_HEAD")
	if err := os.WriteFile(busy, []byte(target+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.step()
	f.step()
	if gitSHA(t, repo, "main") != main || len(f.events(comparisonApprovalInvalidated)) != 0 ||
		len(f.events(comparisonGitHeld)) != 1 || len(f.events(comparisonGitEntryEvent)) != 0 {
		t.Fatal("busy repository invalidated exact approval, entered Git, or was not durably held")
	}
	if err := os.Remove(busy); err != nil {
		t.Fatal(err)
	}
	f.step()
	if f.objective().Status != model.ObjectiveDone || gitSHA(t, repo, "main") != target ||
		len(questionsKind(f, model.QuestionDone)) != 1 {
		t.Fatal("making primary eligible did not consume the original exact valid approval")
	}
}

func TestAPI2LateObservedIntegrationDriftCannotReviveOldApprovalAfterReset(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a local Git shell fixture")
	}
	f, repo, main := api2CompletionFixture(t)
	c := api2StandaloneCard(f, nil)
	api2HoldUnlaunchedCard(f, c)
	original := gitSHA(t, repo, "integration")
	moved := changeComparisonIntegration(t, repo, "late-ref-candidate", "fixture\n")
	gitRun(t, repo, "update-ref", "refs/heads/integration", original, moved)
	f.step()
	old := pendingComparisonApproval(t, f, original, "")
	approveComparisonFixture(t, f, old)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	probe := filepath.Join(dir, "ran")
	// This synthetic shim changes only the test repo and only when the
	// second writer actually reaches the exact update-ref subprocess.
	script := "#!/bin/sh\ncase \" $* \" in\n" +
		"  *' update-ref '*' --stdin '*)\n" +
		"    " + strconv.Quote(realGit) + " -C " + strconv.Quote(repo) + " update-ref refs/heads/integration " + moved + " " + original + " || exit 95\n" +
		"    printf '%s\\n' late > " + strconv.Quote(probe) + " || exit 96;;\n" +
		"esac\nexec " + strconv.Quote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
	f.step()
	t.Setenv("PATH", oldPath)
	if got, err := os.ReadFile(probe); err != nil || string(got) != "late\n" ||
		gitSHA(t, repo, "integration") != moved || gitSHA(t, repo, "main") != main {
		t.Fatalf("late physical ref change missed or unexpectedly updated the primary: %s %v", got, err)
	}
	revoked := f.events(comparisonApprovalInvalidated)
	if len(revoked) != 1 || !strings.Contains(string(revoked[0].Data), "integration_changed_during_git_validation") ||
		len(f.events(comparisonGitEntryClosedEvent)) != 1 {
		t.Fatal("worker's observed late drift did not durably revoke the approval")
	}
	gitRun(t, repo, "update-ref", "refs/heads/integration", original, moved)
	f.step()
	refreshed := pendingComparisonApproval(t, f, original, old.ID)
	if gitSHA(t, repo, "main") != main || refreshed.ID == old.ID {
		t.Fatal("reset revived previously approved exact commit authority")
	}
	approveComparisonFixture(t, f, refreshed)
	f.step()
	if f.objective().Status != model.ObjectiveDone || gitSHA(t, repo, "main") != original {
		t.Fatal("fresh exact reset approval did not advance")
	}
}

func TestAPI2CanceledContextAfterCommittedIntentSuppressesAllReentry(t *testing.T) {
	f, repo, main := api2CompletionFixture(t)
	c := api2StandaloneCard(f, nil)
	api2HoldUnlaunchedCard(f, c)
	f.step()
	original := gitSHA(t, repo, "integration")
	q := pendingComparisonApproval(t, f, original, "")
	approveComparisonFixture(t, f, q)
	var entry *comparisonGitEntry
	// Run the same phase-1 implementation under a real writer and cause
	// cancellation exactly at its committed boundary, before phase 2.
	if err := f.db.WithWriteTx(f.ctx, func(tx context.Context) error {
		fresh, err := loadSnap(tx, f.db, f.obj.ID)
		if err != nil {
			return err
		}
		st := &step{ctx: tx, db: f.db, home: f.home, snap: fresh, now: clockFrom(tx), completionReserved: true}
		err = st.complete()
		entry = st.comparisonPrepared
		return err
	}); err != nil || entry == nil || len(f.events(comparisonGitEntryEvent)) != 1 {
		t.Fatalf("the exact intent was not durably committed: %v", err)
	}
	cancelled, cancel := context.WithCancel(f.ctx)
	cancel()
	if err := executeComparisonEntry(cancelled, f.db, f.obj.ID, *entry, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled phase 2 did not stop: %v", err)
	}
	if err := ResumeOptionalComparisonCandidate(f.ctx, f.db, f.objective()); !errors.Is(err, errPendingComparisonGitEntry) {
		t.Fatalf("fresh scheduler implicitly reused another call's committed intent: %v", err)
	}
	changeComparisonIntegration(t, repo, "after-unknown", "fixture\n")
	f.step()
	if len(f.events(comparisonGitEntryEvent)) != 1 || len(f.events(comparisonGitEntryClosedEvent)) != 0 ||
		len(f.events(comparisonApprovalInvalidated)) != 0 || len(questionsKind(f, model.QuestionDone)) != 1 ||
		gitSHA(t, repo, "main") != main {
		t.Fatal("canceled entry retried, reapproved, or acted on the primary")
	}
	assertAPI2OrdinaryResumeDoesNotReconcile(t, f, repo, main)
}

func assertAPI2OrdinaryResumeDoesNotReconcile(t *testing.T, f *fx, repo, main string) {
	t.Helper()
	acceptanceRan := filepath.Join(t.TempDir(), "must-not-run-acceptance")
	f.setCommands([]string{"test -f marker && printf 'called\n' > " + strconv.Quote(acceptanceRan)})
	o := f.objective()
	o.Status = model.ObjectivePaused
	if err := f.db.UpdateObjective(f.ctx, &o); err != nil {
		t.Fatal(err)
	}
	if err := DevStep(f.ctx, o.ID); err != nil || f.objective().Status != model.ObjectivePaused {
		t.Fatalf("an explicitly suspended objective was affected by the fence: %v", err)
	}
	// This is the exact persisted status assignment of ordinary objective
	// resume, not an operator Git-reconciliation action.
	o = f.objective()
	o.Status = model.ObjectiveActive
	if err := f.db.UpdateObjective(f.ctx, &o); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := DevStep(f.ctx, o.ID); !errors.Is(err, errPendingComparisonGitEntry) {
			t.Fatalf("plain Active resume lost the persisted diagnostic: %v", err)
		}
	}
	if _, err := os.Stat(acceptanceRan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plain resume reran acceptance without reconciliation: %v", err)
	}
	if f.objective().Status != model.ObjectiveActive || gitSHA(t, repo, "main") != main ||
		len(questionsKind(f, model.QuestionDone)) != 1 || len(f.events(comparisonApprovalInvalidated)) != 0 ||
		len(f.events(comparisonGitEntryClosedEvent)) != 0 || len(f.events("merged_local")) != 0 {
		t.Fatal("plain resume reapproved, reconciled, or moved the primary")
	}
}

func TestAPI2StaleUnmarkedCompletionCannotBypassOtherWritersEntry(t *testing.T) {
	for _, pinnedTransaction := range []bool{false, true} {
		name := "pre-transaction-inventory"
		if pinnedTransaction {
			name = "older-WAL-snapshot"
		}
		t.Run(name, func(t *testing.T) {
			f, repo, main := api2CompletionFixture(t)
			card := api2StandaloneCard(f, nil)
			api2HoldUnlaunchedCard(f, card)
			probe := filepath.Join(t.TempDir(), "executed-acceptance")
			f.setCommands([]string{"test -f marker && printf 'called\n' > " + strconv.Quote(probe)})
			ctx, cancel := context.WithTimeout(f.ctx, 12*time.Second)
			defer cancel()
			ready, next, result := make(chan error, 1), make(chan struct{}), make(chan error, 1)
			go func() {
				waitForOtherWriter := func(s *snap, err error) error {
					if err == nil {
						if _, marked := s.comparisonCompletionReceipt(); marked || s.obj.Status != model.ObjectiveActive {
							err = errors.New("old snapshot was not an unmarked Active objective")
						}
					}
					ready <- err
					if err != nil {
						return err
					}
					select {
					case <-next:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				runStale := func(tx context.Context, stale *snap) error {
					st := &step{ctx: tx, db: f.db, home: f.home, snap: stale, now: clockFrom(tx)}
					return st.complete()
				}
				if pinnedTransaction {
					result <- f.db.WithTx(ctx, func(tx context.Context) error {
						stale, err := loadSnap(tx, f.db, f.obj.ID)
						if err := waitForOtherWriter(stale, err); err != nil {
							return err
						}
						return runStale(tx, stale)
					})
					return
				}
				stale, err := loadSnap(ctx, f.db, f.obj.ID)
				if err := waitForOtherWriter(stale, err); err != nil {
					result <- err
					return
				}
				result <- f.db.WithTx(ctx, func(tx context.Context) error { return runStale(tx, stale) })
			}()
			select {
			case err := <-ready:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("old worker did not take its fixture snapshot")
			}
			// A separate real writer completes normal qualification,
			// records exact approval and commits the pre-Git entry.
			f.t.Setenv("CARDEX_HOME", f.home)
			if err := DevStep(ctx, f.obj.ID); err != nil {
				t.Fatal(err)
			}
			q := pendingComparisonApproval(t, f, gitSHA(t, repo, "integration"), "")
			approveComparisonFixture(t, f, q)
			if err := f.db.WithTopLevelWriteTx(ctx, func(tx context.Context) error {
				s, err := loadSnap(tx, f.db, f.obj.ID)
				if err != nil {
					return err
				}
				st := &step{ctx: tx, db: f.db, home: f.home, snap: s, now: clockFrom(tx), completionReserved: true}
				if err := st.complete(); err != nil {
					return err
				}
				if st.comparisonPrepared == nil {
					return errors.New("missing exact comparison entry")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// Abandon that process's entry and use normal resume while the
			// old unmarked objective snapshot is still in flight.
			o := f.objective()
			o.Status = model.ObjectiveActive
			if err := f.db.UpdateObjective(ctx, &o); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(probe); err != nil {
				t.Fatal("fresh legitimate qualification did not run:", err)
			}
			close(next)
			err := <-result
			if pinnedTransaction {
				if err == nil || !strings.Contains(strings.ToLower(err.Error()), "locked") {
					t.Fatalf("stale WAL view did not fail closed at its writer upgrade: %v", err)
				}
			} else if !errors.Is(err, errPendingComparisonGitEntry) {
				t.Fatalf("stale nontransactional inventory hid committed fence: %v", err)
			}
			if _, err := os.Stat(probe); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("stale generic objective ran acceptance: %v", err)
			}
			if len(questionsKind(f, model.QuestionDone)) != 1 || len(f.events(comparisonGitEntryEvent)) != 1 ||
				len(f.events(comparisonGitEntryClosedEvent)) != 0 || len(f.events("merged_local")) != 0 ||
				gitSHA(t, repo, "main") != main {
				t.Fatal("stale generic objective reopened or acted on the unresolved candidate")
			}
		})
	}
}

func TestAPI2ManualMarkedApprovalDoesNotRunGenericDevStepHooks(t *testing.T) {
	f, repo, _ := api2CompletionFixture(t)
	c := api2StandaloneCard(f, nil)
	api2HoldUnlaunchedCard(f, c)
	f.step()
	target := gitSHA(t, repo, "integration")
	q := pendingComparisonApproval(t, f, target, "")
	approveComparisonFixture(t, f, q)
	obj := f.objective()
	obj.Mode = model.ModeManual
	if err := f.db.UpdateObjective(f.ctx, &obj); err != nil {
		t.Fatal(err)
	}
	done := f.mustCard("finished")
	done.Budget = model.Budget{}
	if err := f.db.UpdateCard(f.ctx, &done); err != nil {
		t.Fatal(err)
	}
	f.step()
	if f.objective().Status != model.ObjectiveDone || gitSHA(t, repo, "main") != target ||
		f.mustCard(done.ID).Budget != (model.Budget{}) ||
		len(f.events(comparisonGitEntryEvent)) != 1 || len(f.events(comparisonGitEntryClosedEvent)) != 1 {
		t.Fatal("manual marked completion ran ordinary lifecycle hooks or missed its dedicated committed entry")
	}
}

func TestAPI2OptionalCompletionDoesNotJoinNestedTransactionBeforeGit(t *testing.T) {
	f, repo, main := api2CompletionFixture(t)
	c := api2StandaloneCard(f, nil)
	api2HoldUnlaunchedCard(f, c)
	f.step()
	q := pendingComparisonApproval(t, f, gitSHA(t, repo, "integration"), "")
	approveComparisonFixture(t, f, q)
	obj := f.objective()
	err := f.db.WithTx(f.ctx, func(tx context.Context) error {
		return ResumeOptionalComparisonCandidate(tx, f.db, obj)
	})
	if err == nil || !strings.Contains(err.Error(), "independent writer") || len(f.events(comparisonGitEntryEvent)) != 0 ||
		gitSHA(t, repo, "main") != main {
		t.Fatal("a nested caller transaction bypassed the committed entry boundary")
	}
}
