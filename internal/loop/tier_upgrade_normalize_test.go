package loop

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestTierUpgradeRejectsOutstandingNormalizationAfterDispose(t *testing.T) {
	for _, kind := range []model.CardKind{model.KindPlan, model.KindReplan} {
		t.Run(string(kind), func(t *testing.T) {
			f := newFx(t, model.ModeAuto)
			src := f.card(model.Card{ID: "src", Kind: kind, Status: model.CardReady})
			// This ambiguous old-format plan carries a tier that would acquire
			// the opposite meaning if copied into a new normalization result.
			disposePassed(t, f, src, "## 可冻结\n待确认\n\n## 新卡\n[{\"kind\":\"dev\",\"title\":\"fixture\",\"tier\":\"T3\"}]\n")
			norm := normalizeOf(t, f.db, f.obj.ID, src.ID)
			if norm == nil || norm.Status != model.CardReady {
				t.Fatalf("old Dispose did not queue a normalizer: %+v", norm)
			}
			events, err := f.db.ListEvents(f.ctx, "attempt:src-d")
			if err != nil {
				t.Fatal(err)
			}
			disposed := false
			for _, event := range events {
				disposed = disposed || event.Type == "dev_disposed"
			}
			if !disposed {
				t.Fatal("fixture should resemble a disposed but not normalized old plan")
			}
			// The caller stores the harvested outcome only after Dispose returns.
			a, err := f.db.GetAttempt(f.ctx, "src-d")
			if err != nil {
				t.Fatal(err)
			}
			a.Outcome = model.OutcomePassed
			if err := f.db.UpdateAttempt(f.ctx, &a); err != nil {
				t.Fatal(err)
			}
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.home, "cardex.db")
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if _, err := raw.Exec("DELETE FROM schema_migrations WHERE version=?", store.TierConventionSchema); err != nil {
				t.Fatal(err)
			}
			if upgraded, err := store.OpenTierUpgrade(path); err == nil || !strings.Contains(err.Error(), "pending legacy normalization") {
				if upgraded != nil {
					_ = upgraded.Close()
				}
				t.Fatalf("pending plan normalization did not block migration: %v", err)
			}
			var n int
			if err := raw.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version=?", store.TierConventionSchema).Scan(&n); err != nil || n != 0 {
				t.Fatalf("migration marker changed: %d %v", n, err)
			}
			if _, err := raw.Exec("UPDATE cards SET status='canceled' WHERE id=?", norm.ID); err != nil {
				t.Fatal(err)
			}
			upgraded, err := store.OpenTierUpgrade(path)
			if err != nil {
				t.Fatalf("canceled and never-executed normalizer should not block activation: %v", err)
			}
			_ = upgraded.Close()
		})
	}
}
