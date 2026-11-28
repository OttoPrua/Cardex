package loop

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
	"github.com/OttoPrua/Cardex/internal/workspace"
)

const (
	comparisonGitEntryEvent       = "objective_optional_comparison_git_entry"
	comparisonGitEntryClosedEvent = "objective_optional_comparison_git_entry_closed"
)

var errPendingComparisonGitEntry = errors.New("optional completion has an unresolved Git entry; reconcile the local refs before reuse")

type comparisonGitEntry struct {
	Token      string                  `json:"entry_id"`
	Question   string                  `json:"question_id"`
	Acceptance comparisonAcceptancePin `json:"acceptance"`
	Primary    string                  `json:"primary_ref"`
	Before     string                  `json:"primary_before"`
}

func (e comparisonGitEntry) valid() bool {
	return len(e.Token) == 64 && exactComparisonOID(e.Token) && e.Question != "" && e.Acceptance.valid() &&
		(e.Primary == "refs/heads/main" || e.Primary == "refs/heads/master") && exactComparisonOID(e.Before)
}

// openComparisonEntry recognizes a persisted mutation boundary. A restarted
// worker can read this fence but cannot decide that no Git command ran. Only
// the same in-memory call that committed an exact entry can proceed.
func (s *snap) openComparisonEntry() (comparisonGitEntry, bool) {
	entries, closed := map[string]comparisonGitEntry{}, map[string]bool{}
	entity := "objective:" + s.obj.ID
	for _, ev := range s.events {
		if ev.Entity != entity {
			continue
		}
		switch ev.Type {
		case comparisonGitEntryEvent:
			var e comparisonGitEntry
			if json.Unmarshal(ev.Data, &e) != nil || !e.valid() || entries[e.Token].Token != "" {
				return comparisonGitEntry{}, true
			}
			entries[e.Token] = e
		case comparisonGitEntryClosedEvent:
			var c struct {
				Token string `json:"entry_id"`
			}
			if json.Unmarshal(ev.Data, &c) != nil || len(c.Token) != 64 || !exactComparisonOID(c.Token) || closed[c.Token] {
				return comparisonGitEntry{}, true
			}
			closed[c.Token] = true
		}
	}
	for token := range closed {
		if entries[token].Token == "" {
			return comparisonGitEntry{}, true
		}
		delete(entries, token)
	}
	if len(entries) > 1 {
		return comparisonGitEntry{}, true
	}
	for _, e := range entries {
		return e, true
	}
	return comparisonGitEntry{}, false
}

func (s *snap) comparisonNeedsReconciliation() bool {
	_, open := s.openComparisonEntry()
	return open || s.comparisonMergeUnknown()
}

func (st *step) saveComparisonEntry(question string, pin comparisonAcceptancePin, primary, before string) error {
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return err
	}
	e := comparisonGitEntry{Token: hex.EncodeToString(entropy[:]), Question: question, Acceptance: pin, Primary: primary, Before: before}
	if err := st.event("objective:"+st.snap.obj.ID, comparisonGitEntryEvent, e); err != nil {
		return err
	}
	st.comparisonPrepared = &e
	return nil
}

func (st *step) closeComparisonEntry(e comparisonGitEntry) error {
	return st.event("objective:"+st.snap.obj.ID, comparisonGitEntryClosedEvent, map[string]string{"entry_id": e.Token})
}

func runOptionalComparisonCandidate(ctx context.Context, db *store.Store, obj model.Objective, automatic bool) error {
	events, err := db.ListEvents(ctx, "objective:"+obj.ID)
	if err != nil {
		return err
	}
	hint := snap{obj: obj, events: events}
	exemption, marked := hint.comparisonCompletionReceipt()
	if !marked || exemption.Question == "" || hint.comparisonMergeUnknown() {
		return nil
	}
	if _, open := hint.openComparisonEntry(); open {
		if automatic {
			return errPendingComparisonGitEntry
		}
		return nil
	}
	q, err := db.GetQuestion(ctx, exemption.Question)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	hint.questions = []model.Question{q}
	approval, _ := hint.doneApproval()
	if !approved(approval.Answer) {
		return nil
	}
	var st *step
	err = db.WithTopLevelWriteTx(ctx, func(tx context.Context) error {
		fresh, err := loadSnap(tx, db, obj.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		q, stillMarked := fresh.doneApproval()
		if (automatic && fresh.obj.Mode != model.ModeAuto) || fresh.obj.Status != model.ObjectiveDoneCandidate ||
			!stillMarked || q.ID != approval.ID || !approved(q.Answer) || fresh.comparisonMergeUnknown() {
			return nil
		}
		if _, open := fresh.openComparisonEntry(); open {
			if automatic {
				return errPendingComparisonGitEntry
			}
			return nil
		}
		st = &step{ctx: tx, db: db, home: db.Home(), snap: fresh, now: clockFrom(ctx), completionReserved: true}
		return st.complete()
	})
	if err != nil || st == nil {
		return err
	}
	if st.comparisonPrepared != nil {
		// The fence above is durably committed before any Git operation.
		// Cancellation here leaves it unresolved, rather than reissuing.
		return executeComparisonEntry(ctx, db, obj.ID, *st.comparisonPrepared, automatic)
	}
	if automatic {
		return st.comparisonDiagnostic // only after the journal write commits
	}
	return nil
}

func executeComparisonEntry(ctx context.Context, db *store.Store, objective string, expected comparisonGitEntry, automatic bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var st *step
	err := db.WithTopLevelWriteTx(ctx, func(tx context.Context) error {
		fresh, err := loadSnap(tx, db, objective)
		if err != nil {
			return err
		}
		entry, open := fresh.openComparisonEntry()
		if !open || entry != expected || fresh.comparisonMergeUnknown() {
			return errPendingComparisonGitEntry
		}
		st = &step{ctx: tx, db: db, home: db.Home(), snap: fresh, now: clockFrom(ctx), completionReserved: true}
		return st.executeComparisonEntry(entry, automatic)
	})
	if err != nil {
		return err
	}
	if automatic && st != nil {
		return st.comparisonDiagnostic // no rollback after a typed hold
	}
	return nil
}

func (st *step) executeComparisonEntry(e comparisonGitEntry, automatic bool) error {
	q, marked := st.snap.doneApproval()
	if (automatic && st.snap.obj.Mode != model.ModeAuto) || st.snap.obj.Status != model.ObjectiveDoneCandidate ||
		!marked || q.ID != e.Question || !approved(q.Answer) ||
		st.snap.hasEvent("question:"+e.Question, comparisonApprovalInvalidated) {
		return st.closeComparisonEntry(e) // no Git entry was attempted
	}
	if ready, err := st.checkComparisonAcceptanceRepo(e.Question); err != nil || !ready {
		if err != nil {
			return err
		}
		return st.closeComparisonEntry(e) // definite no Git entry
	}
	p, ok := st.currentComparisonAcceptance()
	if !ok || p != e.Acceptance {
		if err := st.invalidateComparisonApproval(e.Question, "tested_candidate_no_longer_matches"); err != nil {
			return err
		}
		return st.closeComparisonEntry(e)
	}
	receipt, _ := st.snap.comparisonCompletionReceipt()
	if !st.allFrozen() {
		return st.closeComparisonEntry(e)
	}
	safe, err := st.priorComparisonCardsSafe(receipt)
	if err != nil {
		return err
	}
	unfinished, _, err := st.anyUnfinished()
	if err != nil {
		return err
	}
	if !safe || unfinished {
		return st.closeComparisonEntry(e)
	}
	err = (workspace.Git{}).MergeOptionalCandidate(st.ctx, st.snap.project.RepoPath, e.Primary, e.Acceptance.Ref, e.Before, e.Acceptance.OID)
	if err == nil {
		if err := st.finishObjectiveMerge(e.Acceptance.OID); err != nil {
			return err // durable entry remains open, never replayed
		}
		return st.closeComparisonEntry(e)
	}
	keepOpen, outcomeErr := st.recordComparisonGitResult(e.Question, e.Acceptance, err)
	if outcomeErr != nil || keepOpen {
		return outcomeErr
	}
	return st.closeComparisonEntry(e)
}
