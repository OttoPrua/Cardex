package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/homeguard"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/roots"
	"github.com/OttoPrua/Cardex/internal/store"
)

type verifyView struct {
	Card              string           `json:"card"`
	Status            model.CardStatus `json:"status"`
	Attempt           string           `json:"attempt,omitempty"`
	Outcome           model.Outcome    `json:"outcome,omitempty"`
	Head              string           `json:"head,omitempty"`
	Result            string           `json:"result"`
	Reason            string           `json:"reason"`
	AcceptanceCount   int              `json:"acceptance_count"`
	AcceptanceMissing int              `json:"acceptance_missing"`
	AcceptanceFailed  int              `json:"acceptance_failed"`
	acceptancePassed  int
}

func cmdVerify(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("verify", "check recorded evidence for the newest card attempt; do not rerun tests or start workers", "<card-id> --json", stdout, stderr)
	if code, ok := parseSet(fs, hoistFlags(fs, args)); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "verify <card-id>")
		return 2
	}
	dir, err := homePath(home)
	if err == nil {
		err = homeguard.Check(dir)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx := context.Background()
	var row verifyView
	var readErr error
	err = store.InspectClean(filepath.Join(dir, "cardex.db"), func(db *store.Store) {
		var card model.Card
		var attempt model.Attempt
		readErr = db.WithTx(ctx, func(readCtx context.Context) error {
			var err error
			card, err = db.GetCard(readCtx, fs.Arg(0))
			if err != nil {
				return err
			}
			attempt, err = latestStoredAttempt(readCtx, db, card.ID)
			return err
		})
		if readErr == nil {
			row = verifyAttempt(dir, card, attempt)
		}
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if readErr != nil {
		fmt.Fprintln(stderr, readErr)
		return 1
	}
	if asJSON {
		if code := writeJSON(stdout, row); code != 0 {
			return code
		}
	} else {
		fmt.Fprintf(stdout, "%s\t%s (card: %s)\t%s\n", clipBriefText(row.Card), row.Result, row.Status, row.Reason)
		if row.Attempt != "" {
			fmt.Fprintf(stdout, "attempt\t%s\t%s\n", clipBriefText(row.Attempt), row.Outcome)
		}
		if row.Head != "" {
			fmt.Fprintf(stdout, "head\t%s\n", clipBriefText(row.Head))
		}
		fmt.Fprintf(stdout, "recorded acceptance\t%d passed / %d total\n", row.acceptancePassed, row.AcceptanceCount)
	}
	if row.Result != "verified" {
		return 1
	}
	return 0
}

func verifyAttempt(home string, card model.Card, a model.Attempt) verifyView {
	v := verifyView{Card: card.ID, Status: card.Status, Attempt: a.ID, Head: a.HeadCommit, Outcome: a.Outcome, Result: "incomplete"}
	fail := func(result, reason string) verifyView { v.Result, v.Reason = result, reason; return v }
	if !cardSafeID(a.ID) {
		return fail("incomplete", "no recorded attempt")
	}
	if a.EndedAt.IsZero() || a.Outcome == "" {
		return fail("incomplete", "newest attempt is not settled")
	}
	if a.Outcome != model.OutcomePassed {
		return fail("failed", "newest attempt did not pass")
	}
	sum, ok, err := latestSummary(home, []model.Attempt{a})
	if err != nil || !ok {
		return fail("incomplete", "newest attempt has no readable summary")
	}
	headMatches := sum.HeadCommit == a.HeadCommit
	if card.Kind == model.KindReview && a.HeadCommit == "" {
		headMatches = cleanReviewHead(home, card, a, sum)
	}
	if sum.AttemptID != a.ID || sum.BaseCommit != a.BaseCommit || !headMatches ||
		sum.Channel != a.Route.Channel || sum.Model != a.Route.Model {
		return fail("failed", "summary does not match the newest attempt")
	}
	if !sum.Match {
		return fail("failed", "recorded routing did not match attempt expectations")
	}
	// Match the currently declared contract, not merely any command in a
	// summary whose provenance fields happen to match. Changes to the card
	// after an attempt conservatively require fresh evidence.
	var declared []string
	for _, command := range card.Acceptance {
		if command = strings.TrimSpace(command); command != "" {
			declared = append(declared, command)
		}
	}
	v.AcceptanceCount = len(declared)
	if len(sum.Acceptance) > len(declared) {
		return fail("failed", "recorded acceptance does not match the card")
	}
	v.AcceptanceMissing = len(declared) - len(sum.Acceptance)
	for i, check := range sum.Acceptance {
		got := strings.TrimSpace(check.Command)
		if got != "" && got != declared[i] {
			return fail("failed", "recorded acceptance does not match the card")
		}
		if got == "" || !check.HasExitCode() {
			v.AcceptanceMissing++
			continue
		}
		if check.Exit != 0 || (check.Baseline != nil && len(check.Baseline.New) > 0) {
			v.AcceptanceFailed++
		} else {
			v.acceptancePassed++
		}
	}
	if v.AcceptanceFailed > 0 {
		return fail("failed", "recorded acceptance checks did not all pass")
	}
	if v.AcceptanceMissing > 0 {
		return fail("incomplete", "recorded acceptance is missing a declared command or exit code")
	}
	if card.Kind == model.KindReview {
		if sum.Review == nil || sum.Review.Verdict == "" || sum.Review.P0 == nil || sum.Review.P1 == nil {
			return fail("incomplete", "no complete structured review verdict")
		}
		if sum.Review.Verdict != "pass" || len(sum.Review.P0) > 0 || len(sum.Review.P1) > 0 {
			return fail("failed", "recorded review did not pass without P0/P1 findings")
		}
	} else if v.AcceptanceCount == 0 {
		return fail("incomplete", "no recorded acceptance command")
	}
	v.Head = sum.HeadCommit
	return fail("verified", "recorded evidence passed; nothing was rerun")
}

// A successful, unchanged review has no commit to write to the attempt
// row. Harvest still records its checkout HEAD. Use that recorded HEAD
// only when it is the same full Git object ID as the attempt's pinned base.
func cleanReviewHead(home string, card model.Card, attempt model.Attempt, summary harvest.Summary) bool {
	if card.Kind != model.KindReview || attempt.HeadCommit != "" || summary.HeadCommit != attempt.BaseCommit ||
		attempt.Exit == nil || *attempt.Exit != 0 || summary.CleanReviewCheckout == nil || !*summary.CleanReviewCheckout ||
		summary.Files == nil || len(summary.Files) != 0 || summary.Review == nil {
		return false
	}
	id := attempt.BaseCommit
	if (len(id) != 40 && len(id) != 64) || strings.Trim(id, "0") == "" || strings.ToLower(id) != id {
		return false
	}
	if _, err := hex.DecodeString(id); err != nil {
		return false
	}
	if summary.Review.Source != roots.EvidenceRef(home, attempt.ID, attempt.PromptPath, "result.md") {
		return false
	}
	resultPath := filepath.Join(filepath.Dir(harvest.SummaryPath(home, attempt)), "result.md")
	fi, err := os.Lstat(resultPath)
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}
