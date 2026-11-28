package store

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

type strongQuotaWindow struct {
	account, channel, name, reset string
	limit, baseline               int64
}

func strongQuotaStamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }

func strongQuotaWindows(c model.Channel, now time.Time) ([]strongQuotaWindow, bool) {
	if !model.StrongKey(c.SourceGroup) || !model.StrongKey(c.ID) || c.Runner != model.RunnerFake || c.Health != model.HealthOK || len(c.QuotaWindows) == 0 || len(c.QuotaWindows) > 8 {
		return nil, false
	}
	seen := map[string]bool{}
	out := make([]strongQuotaWindow, 0, len(c.QuotaWindows))
	for _, w := range c.QuotaWindows {
		name := strings.ToLower(w.Name)
		if !model.StrongKey(name) || seen[name] || w.LimitTokens <= 0 || w.LimitTokens > model.StrongTokenCeiling || w.UsedTokens < 0 || w.UsedTokens > w.LimitTokens ||
			w.LimitRequests != 0 || w.Confidence != 1 || !now.Before(w.ResetAt) || !(w.Remaining > max(0, c.Redline)) || w.Remaining > 1 {
			return nil, false
		}
		seen[name] = true
		capacity := min(w.LimitTokens-w.UsedTokens, int64(math.Floor(w.Remaining*float64(w.LimitTokens))))
		out = append(out, strongQuotaWindow{strings.ToLower(c.SourceGroup), strings.ToLower(c.ID), name, strongQuotaStamp(w.ResetAt), w.LimitTokens, w.LimitTokens - capacity})
	}
	slices.SortFunc(out, func(a, b strongQuotaWindow) int { return strings.Compare(a.name, b.name) })

	return out, true
}

func (s *Store) strongQuotaAvailable(ctx context.Context, c model.Channel, intent string, reserve int64, requireHold bool, now time.Time) bool {
	windows, ok := strongQuotaWindows(c, now)
	if !ok || reserve <= 0 {
		return false
	}
	var owned int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT count(*) FROM strong_run_quota_holds WHERE intent_id=? AND state='held'`, intent).Scan(&owned); err != nil ||
		(requireHold && owned != len(windows)) || (owned != 0 && owned != len(windows)) {
		return false
	}
	if owned != 0 {
		if _, ok := s.strongHeldQuota(ctx, intent, reserve); !ok {
			return false
		}
	}
	known := map[[2]string]bool{}
	for _, w := range windows {
		known[[2]string{w.name, w.reset}] = true
	}
	active, err := s.q(ctx).QueryContext(ctx, `SELECT DISTINCT window_name,reset_at FROM strong_run_quota_holds
	   WHERE account=? AND (state='held' OR (state='measured' AND reset_at>?)) LIMIT 9`, windows[0].account, strongQuotaStamp(now))
	if err != nil {
		return false
	}
	for active.Next() {
		var name, reset string
		if err := active.Scan(&name, &reset); err != nil || !known[[2]string{name, reset}] {
			_ = active.Close()
			return false
		}
	}
	if err := active.Err(); err != nil {
		_ = active.Close()
		return false
	}
	if err := active.Close(); err != nil {
		return false
	}

	for _, w := range windows {
		var mismatch int
		if err := s.q(ctx).QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM strong_run_quota_holds
		   WHERE channel=? AND account<>? AND (state='held' OR (state='measured' AND reset_at>?)))`, w.channel, w.account, strongQuotaStamp(now)).Scan(&mismatch); err != nil || mismatch != 0 {
			return false
		}
		if err := s.q(ctx).QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM strong_run_quota_holds WHERE account=? AND window_name=?
		   AND reset_at<>? AND (state='held' OR (state='measured' AND reset_at>?)))`, w.account, w.name, w.reset, strongQuotaStamp(now)).Scan(&mismatch); err != nil || mismatch != 0 {
			return false
		}
		if owned != 0 {
			var exact int
			if err := s.q(ctx).QueryRowContext(ctx, `SELECT count(*) FROM strong_run_quota_holds WHERE intent_id=? AND state='held'
			 AND account=? AND channel=? AND window_name=? AND reset_at=? AND limit_tokens=? AND tokens=?`, intent, w.account, w.channel, w.name, w.reset, w.limit, reserve).Scan(&exact); err != nil || exact != 1 {
				return false
			}
		}
		var consumed, baseline, minLim, maxLim int64
		if err := s.q(ctx).QueryRowContext(ctx, `SELECT COALESCE(sum(CASE WHEN intent_id<>? THEN tokens ELSE 0 END),0),
		   COALESCE(max(baseline_tokens),0),COALESCE(min(limit_tokens),?),COALESCE(max(limit_tokens),?)
		   FROM strong_run_quota_holds WHERE account=? AND window_name=? AND reset_at=? AND state IN ('held','measured')`,
			intent, w.limit, w.limit, w.account, w.name, w.reset).Scan(&consumed, &baseline, &minLim, &maxLim); err != nil ||
			minLim != w.limit || maxLim != w.limit || consumed < 0 || reserve > w.limit-max(baseline, w.baseline)-consumed {
			return false
		}
	}
	return true
}

func (s *Store) reserveStrongQuota(ctx context.Context, p model.StrongPlan, intent StrongIntent) error {
	if !p.CrossFamilyReview {
		return nil
	}
	c, err := s.GetChannel(ctx, intent.Route.Channel)
	if err != nil {
		return err
	}
	now := time.Now()
	windows, ok := strongQuotaWindows(c, now)
	if !ok || !s.strongQuotaAvailable(ctx, c, intent.ID, intent.Reserved, false, now) {
		return model.ErrStrongPlan
	}
	if _, err := s.q(ctx).ExecContext(ctx, `INSERT INTO strong_run_quota_manifests(intent_id,count,sha) VALUES(?,?,?)`, intent.ID, len(windows), strongQuotaDigest(windows, intent.Reserved)); err != nil {
		return err
	}

	for _, w := range windows {
		_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO strong_run_quota_holds(intent_id,account,channel,window_name,reset_at,limit_tokens,baseline_tokens,tokens,state)
		  VALUES(?,?,?,?,?,?,?,?,'held')`, intent.ID, w.account, w.channel, w.name, w.reset, w.limit, w.baseline, intent.Reserved)
		if err != nil {
			return err
		}
	}
	return nil
}

func strongQuotaDigest(windows []strongQuotaWindow, reserved int64) string {
	type identity struct {
		Account, Channel, Name, Reset string
		Limit, Baseline               int64
	}
	x := make([]identity, len(windows))
	for i, w := range windows {
		x[i] = identity{w.account, w.channel, w.name, w.reset, w.limit, w.baseline}
	}
	raw, _ := json.Marshal(struct {
		Held    int64
		Windows []identity
	}{reserved, x})
	return model.StrongArtifactDigest(raw)
}

// strongHeldQuota verifies the immutable historically held windows. It does
// not reread today's mutable configuration when attributing known old spend.
func (s *Store) strongHeldQuota(ctx context.Context, id string, reserved int64) (int, bool) {
	windows, ok := s.strongQuotaHistory(ctx, id, reserved, reserved, "held")
	return len(windows), ok
}

func (s *Store) strongQuotaHistory(ctx context.Context, id string, reserved, measured int64, wantState string) ([]strongQuotaWindow, bool) {
	var count int
	var sha string
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT count,sha FROM strong_run_quota_manifests WHERE intent_id=?`, id).Scan(&count, &sha); err != nil || count < 1 || count > 8 {
		return nil, false
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT account,channel,window_name,reset_at,limit_tokens,baseline_tokens,tokens,state
		 FROM strong_run_quota_holds WHERE intent_id=? ORDER BY window_name LIMIT 9`, id)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	out := make([]strongQuotaWindow, 0, count)
	for rows.Next() {
		var w strongQuotaWindow
		var tokens int64
		var state string
		if err := rows.Scan(&w.account, &w.channel, &w.name, &w.reset, &w.limit, &w.baseline, &tokens, &state); err != nil || tokens != measured || state != wantState {
			return nil, false
		}
		out = append(out, w)
	}
	if rows.Err() != nil || len(out) != count || strongQuotaDigest(out, reserved) != sha {
		return nil, false
	}
	return out, true
}

func (s *Store) settleStrongQuota(ctx context.Context, p model.StrongPlan, in StrongIntent, tokens int64) (bool, error) {
	if !p.CrossFamilyReview {
		return true, nil
	}
	count, ok := s.strongHeldQuota(ctx, in.ID, in.Reserved)
	if !ok {
		return false, nil
	}
	r, err := s.q(ctx).ExecContext(ctx, `UPDATE strong_run_quota_holds SET state='measured',tokens=? WHERE intent_id=? AND state='held'`, tokens, in.ID)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	if err != nil || n != int64(count) {
		return false, model.ErrStrongPlan
	}
	return true, nil
}

// releaseStrongNoStart consumes the exact claimed lease without a process.
// Only the transition to starting would allow a child; this one cannot race
// that transition because the same SQLite writer protects both paths.
func (s *Store) releaseStrongNoStart(ctx context.Context, in StrongIntent, lease, why string) error {
	count, ok := s.strongHeldQuota(ctx, in.ID, in.Reserved)
	if !ok {
		return model.ErrStrongPlan
	}
	r, err := s.q(ctx).ExecContext(ctx, `UPDATE strong_run_intents SET state=? WHERE intent_id=? AND state=? AND lease=?`, strongStopped, in.ID, strongClaimed, lease)
	if err := rowsAffected(r, err); err != nil {
		return err
	}
	r, err = s.q(ctx).ExecContext(ctx, `UPDATE strong_run_plans SET reserved=reserved-? WHERE run_id=? AND revision=? AND reserved>=?`, in.Reserved, in.Key.Run, in.Key.Revision, in.Reserved)
	if err := rowsAffected(r, err); err != nil {
		return err
	}
	r, err = s.q(ctx).ExecContext(ctx, `UPDATE strong_run_quota_holds SET state='released' WHERE intent_id=? AND state='held'`, in.ID)
	if err != nil {
		return err
	}
	if n, err := r.RowsAffected(); err != nil || n != int64(count) {
		return model.ErrStrongPlan
	}
	if err := s.setStrongSlotState(ctx, in.Key, in.Slot, strongStopped, why); err != nil {
		return err
	}
	return s.setStrongRunState(ctx, in.Key, StrongBlocked, why)
}
