package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

const (
	StrongReasonReviewFamilyUnknown = "review_family_unknown"
	StrongReasonReviewSameFamily    = "review_same_family"
	StrongReasonReviewSameModel     = "review_same_concrete_model"
	StrongReasonReviewSameChannel   = "review_same_concrete_channel"
	StrongReasonReviewQuota         = "review_quota_unavailable"
	StrongReasonReviewAuthor        = "rewrite_not_actual_author"
	StrongReasonReviewLineage       = "review_source_invalid"
	StrongCompleted                 = "synthetic_completed"
	StrongUnresolved                = "synthetic_completed_with_unresolved"
)

type strongReviewPin struct {
	Author   string
	Seat     string
	Ref      model.StrongArtifactRef
	Route    model.StrongApprovedRoute
	Concrete model.StrongConcreteRoute
	Evidence string
	Account  string
}

func knownStrongFamily(s string) bool {
	if !model.StrongKey(s) {
		return false
	}
	switch strings.ToLower(s) {
	case "unknown", "unspecified", "none", "other", "auto", "default", "unidentified", "na":
		return false
	}
	return true
}

func (s *Store) writeStrongReviewPin(ctx context.Context, p model.StrongPlan, in StrongIntent, slot model.StrongSlot, ref model.StrongArtifactRef, att model.Attempt) error {
	if !p.CrossFamilyReview || slot.Key != p.StrongAuthorSlot() {
		return nil
	}
	if !actualSyntheticStrongRoute(att, in.Route) || !knownStrongFamily(in.Route.Family) || !model.StrongKey(slot.Seat) {
		return model.ErrStrongArtifact
	}
	account, ok := s.strongMeasuredAccount(ctx, in, att.Usage.Tokens)
	if !ok {
		return model.ErrStrongArtifact
	}
	pin := strongReviewPin{Author: slot.Key, Seat: slot.Seat, Ref: ref, Route: in.Route, Concrete: in.Route.Concrete(), Evidence: p.EvidenceSHA, Account: account}
	body, err := json.Marshal(pin)
	if err != nil {
		return err
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO strong_run_review_pin(run_id,revision,plan_sha,body,body_sha)
	  VALUES(?,?,?,?,?)`, in.Key.Run, in.Key.Revision, in.Key.SHA, body, model.StrongArtifactDigest(body))
	return err
}

func (s *Store) readStrongReviewPin(ctx context.Context, p model.StrongPlan, key StrongRunKey) (strongReviewPin, error) {
	var pin strongReviewPin
	var raw []byte
	var sha string
	err := s.q(ctx).QueryRowContext(ctx, `SELECT CASE WHEN length(body)<=4096 THEN body END,body_sha FROM strong_run_review_pin
	  WHERE run_id=? AND revision=? AND plan_sha=?`, key.Run, key.Revision, key.SHA).Scan(&raw, &sha)
	if err != nil || model.StrongArtifactDigest(raw) != sha || json.Unmarshal(raw, &pin) != nil || pin.Author != p.StrongAuthorSlot() ||
		pin.Evidence != p.EvidenceSHA || pin.Concrete != pin.Route.Concrete() || !knownStrongFamily(pin.Route.Family) {
		return strongReviewPin{}, model.ErrStrongArtifact
	}
	canon, err := json.Marshal(pin)
	if err != nil || !bytes.Equal(canon, raw) {
		return strongReviewPin{}, model.ErrStrongArtifact
	}
	author, ok := p.Slot(pin.Author)
	if !ok || author.Seat != pin.Seat || !slices.Contains(p.Limits[author.Key].Routes, pin.Route) {
		return strongReviewPin{}, model.ErrStrongArtifact
	}
	ref, state, _, err := s.strongSlotArtifact(ctx, p, key, pin.Author)
	if err != nil || state != strongDone || ref != pin.Ref {
		return strongReviewPin{}, model.ErrStrongArtifact
	}
	in, err := s.strongIntent(ctx, key, pin.Author)
	if err != nil || in.Route != pin.Route || in.Attempt != ref.Attempt {
		return strongReviewPin{}, model.ErrStrongArtifact
	}
	att, err := s.GetAttempt(ctx, in.Attempt)
	if err != nil || att.Card != in.Card || !actualSyntheticStrongRoute(att, pin.Route) {
		return strongReviewPin{}, model.ErrStrongArtifact
	}
	if account, ok := s.strongMeasuredAccount(ctx, in, att.Usage.Tokens); !ok || account != pin.Account {
		return strongReviewPin{}, model.ErrStrongArtifact
	}
	return pin, nil
}

func (s *Store) strongMeasuredAccount(ctx context.Context, in StrongIntent, measured int64) (string, bool) {
	windows, ok := s.strongQuotaHistory(ctx, in.ID, in.Reserved, measured, "measured")
	if !ok || !model.StrongKey(windows[0].account) {
		return "", false
	}
	account := windows[0].account
	for _, w := range windows {
		if w.account != account || w.channel != strings.ToLower(in.Route.Channel) {
			return "", false
		}
	}
	return account, true
}

func (p strongReviewPin) independent(route model.StrongApprovedRoute) string {
	if p.Concrete.ModelKey() == route.Concrete().ModelKey() || p.Concrete == route.Concrete() {
		return StrongReasonReviewSameModel
	}
	if p.Concrete.Runner == route.Concrete().Runner && p.Concrete.Channel == route.Concrete().Channel {
		return StrongReasonReviewSameChannel
	}
	if strings.EqualFold(p.Route.Family, route.Family) {
		return StrongReasonReviewSameFamily
	}
	return ""
}

func (s *Store) strongST3RouteGate(ctx context.Context, p model.StrongPlan, key StrongRunKey, slot model.StrongSlot, route model.StrongApprovedRoute, requireQuotaHold bool) string {
	if !p.CrossFamilyReview {
		return ""
	}
	if route.Runner != string(model.RunnerFake) || route.T0 {
		return StrongReasonIsolation
	}
	var author strongReviewPin
	if slot.Phase == model.StrongPhaseReview || slot.Phase == model.StrongPhaseRewrite {
		var err error
		author, err = s.readStrongReviewPin(ctx, p, key)
		if err != nil {
			return StrongReasonReviewLineage
		}
		if slot.Phase == model.StrongPhaseReview {
			if why := author.independent(route); why != "" {
				return why
			}
		}
		if slot.Phase == model.StrongPhaseRewrite && (route != author.Route || slot.Seat != author.Seat) {
			return StrongReasonReviewAuthor
		}
	}
	c, err := s.GetChannel(ctx, route.Channel)
	if err != nil || c.Runner != model.RunnerFake || !knownStrongFamily(c.Family) || !strings.EqualFold(c.Family, route.Family) ||
		!slices.Contains(c.Models, route.Model) || !model.StrongKey(c.SourceGroup) {
		return StrongReasonReviewFamilyUnknown
	}
	if slot.Phase == model.StrongPhaseRewrite && strings.ToLower(c.SourceGroup) != author.Account {
		return StrongReasonReviewAuthor
	}
	rows, err := s.ListPositioning(ctx)
	if err != nil {
		return StrongReasonReviewFamilyUnknown
	}
	kind := model.KindDesign
	if slot.Phase == model.StrongPhaseReview {
		kind = model.KindReview
	}
	found := false
	for _, row := range rows {
		if !strings.EqualFold(row.Model, route.Model) {
			continue
		}
		if !knownStrongFamily(row.Family) || !strings.EqualFold(row.Family, route.Family) {
			return StrongReasonReviewFamilyUnknown
		}
		if row.Model == route.Model && (len(row.Kinds) == 0 || slices.Contains(row.Kinds, kind)) {
			found = true
		}
	}
	if !found {
		return StrongReasonReviewFamilyUnknown
	}
	id, _, _ := strongIntentIDs(key, slot.Key)
	if !s.strongQuotaAvailable(ctx, c, id, p.Limits[slot.Key].Tokens, requireQuotaHold, time.Now()) {
		return StrongReasonReviewQuota
	}
	return ""
}

// checkKnownReviewIdentities denies exact known identity tokens without
// rewriting sealed bytes. It is not a claim that arbitrary free text is
// anonymous or that all possible names/aliases can be recognized.
func checkKnownReviewIdentities(p model.StrongPlan, bodies ...[]byte) error {
	tokens := map[string]bool{}
	for _, key := range []string{p.StrongAuthorSlot(), "review"} {
		for _, r := range p.Limits[key].Routes {
			tokens[strings.ToLower(r.Channel)] = true
			tokens[strings.ToLower(r.Model)] = true
		}
	}
	for _, body := range bodies {
		for _, part := range strings.FieldsFunc(string(body), func(c rune) bool {
			return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_')
		}) {
			if tokens[strings.ToLower(part)] {
				return model.ErrStrongArtifact
			}
		}
	}
	return nil
}

func (s *Store) checkStrongReviewBundleIdentities(ctx context.Context, target model.StrongStage, bodies ...[]byte) error {
	if target.Role != model.StrongWorkflowReview && target.Role != model.StrongWorkflowRewrite {
		return nil
	}
	prefix, _, ok := strings.Cut(target.Stage, "-")
	if !ok {
		return model.ErrStrongArtifact
	}
	var revision int
	if _, err := fmt.Sscanf(prefix, "v%d", &revision); err != nil || revision < 1 || prefix != fmt.Sprintf("v%d", revision) {
		return model.ErrStrongArtifact
	}
	key := StrongRunKey{Run: target.Run, Revision: revision}
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT plan_sha FROM strong_run_plans WHERE run_id=? AND revision=?`, key.Run, key.Revision).Scan(&key.SHA); err != nil {
		return model.ErrStrongArtifact
	}
	p, _, err := s.strongPlan(ctx, key)
	if err != nil || !p.CrossFamilyReview {
		return model.ErrStrongArtifact
	}
	return checkKnownReviewIdentities(p, bodies...)
}
