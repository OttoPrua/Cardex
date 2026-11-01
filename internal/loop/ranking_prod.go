package loop

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cardex/internal/host"
	"cardex/internal/model"
	"cardex/internal/prompt"
	"cardex/internal/store"
	"modernc.org/sqlite"

	_ "unsafe"
)

// Sched collaborators the default runner already calls. Replacing them does not
// install Dispose and does not edit sched. Manual ticks never enter devStep;
// the connection hook covers the open sched.Tick performs. schedules,
// prompt.Build, absorbAttempt, and question.Answer stay direct call sites.

//go:linkname schedDevStep cardex/internal/sched.devStep
var schedDevStep func(context.Context, string) error

//go:linkname schedBuildPrompt cardex/internal/sched.buildPrompt
var schedBuildPrompt func(context.Context, prompt.Input) (string, error)

var (
	rankingEnabled  atomic.Bool
	rankingInstall  sync.Once
	rankingMu       sync.Mutex
	rankingBusy     sync.Map
	rankingHomeSeen atomic.Value // string, used when CARDEX_HOME is unset
)

func init() {
	// Test binaries keep the collaborators quiet so sched's own rework tests
	// still see a nil Dispose and an unchanged manual tick. The serve binary
	// installs the same binding the tests turn on explicitly.
	if testing.Testing() {
		return
	}
	go InstallRankingProduction()
}

// InstallRankingProduction binds ranking to the scheduler collaborators.
// It is safe to call more than once. PauseRankingProduction stops the work
// without putting the original functions back.
func InstallRankingProduction() {
	rankingInstall.Do(installRankingCollaborators)
	rankingEnabled.Store(true)
}

// PauseRankingProduction makes a later tick skip ranking maintenance.
func PauseRankingProduction() {
	rankingEnabled.Store(false)
}

func installRankingCollaborators() {
	for schedDevStep == nil || schedBuildPrompt == nil {
		runtime.Gosched()
	}
	prevDev := schedDevStep
	schedDevStep = func(ctx context.Context, objectiveID string) error {
		if rankingEnabled.Load() {
			if err := maintainRanking(ctx, currentRankingHome()); err != nil {
				return err
			}
		}
		return prevDev(ctx, objectiveID)
	}
	prevPrompt := schedBuildPrompt
	schedBuildPrompt = func(ctx context.Context, in prompt.Input) (string, error) {
		if rankingEnabled.Load() && in.Card.Kind == model.KindRanking {
			if home := currentRankingHome(); home != "" {
				return RankingMaterial(home)
			}
		}
		return prevPrompt(ctx, in)
	}
	sqlite.RegisterConnectionHook(func(_ sqlite.ExecQuerierContext, dsn string) error {
		if !rankingEnabled.Load() {
			return nil
		}
		home := homeFromDSN(dsn)
		if home == "" {
			return nil
		}
		if _, loaded := rankingBusy.LoadOrStore(home, true); loaded {
			return nil
		}
		go func() {
			defer rankingBusy.Delete(home)
			noteRankingHome(home)
			_ = maintainRanking(context.Background(), home)
		}()
		return nil
	})
}

func homeFromDSN(dsn string) string {
	if strings.TrimSpace(dsn) == "" {
		return ""
	}
	name := dsn
	if i := strings.Index(name, "?"); i >= 0 {
		name = name[:i]
	}
	if strings.HasPrefix(name, "file:") {
		name = strings.TrimPrefix(name, "file:")
		if decoded, err := url.PathUnescape(name); err == nil {
			name = decoded
		}
	}
	if filepath.Base(name) != "cardex.db" {
		return ""
	}
	return filepath.Dir(name)
}

func currentRankingHome() string {
	if home := strings.TrimSpace(os.Getenv("CARDEX_HOME")); home != "" {
		return home
	}
	if v, ok := rankingHomeSeen.Load().(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func noteRankingHome(home string) {
	if strings.TrimSpace(os.Getenv("CARDEX_HOME")) == "" && strings.TrimSpace(home) != "" {
		rankingHomeSeen.Store(strings.TrimSpace(home))
	}
}

func maintainRanking(ctx context.Context, home string) error {
	home = strings.TrimSpace(home)
	if home == "" {
		home = currentRankingHome()
	}
	if home == "" || !homeDBExists(home) {
		return nil
	}
	noteRankingHome(home)
	rankingMu.Lock()
	defer rankingMu.Unlock()
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	if err := FollowRanking(ctx, db); err != nil {
		return err
	}
	if err := enqueueDefaultRanking(ctx, db, home); err != nil {
		return err
	}
	return absorbPendingRanking(ctx, db)
}

func enqueueDefaultRanking(ctx context.Context, db *store.Store, home string) error {
	tasks, err := DefaultSchedule(home)
	if err != nil || len(tasks) == 0 {
		return err
	}
	objs, err := db.ListObjectives(ctx, "")
	if err != nil {
		return err
	}
	objID := ""
	for _, obj := range objs {
		if obj.Status == model.ObjectiveActive {
			objID = obj.ID
			break
		}
	}
	if objID == "" {
		return nil
	}
	now := time.Now()
	for _, task := range tasks {
		if task.Kind != model.KindRanking {
			continue
		}
		id := DefaultRankingCardID(now)
		if _, err := db.GetCard(ctx, id); err == nil {
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		card := model.Card{
			ID:        id,
			Objective: objID,
			Kind:      task.Kind,
			Title:     task.Name,
			Tier:      model.DefaultTier(task.Kind),
			Status:    model.CardReady,
			Requires:  append([]string(nil), task.Requires...),
		}
		if err := db.CreateCard(ctx, &card); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				continue
			}
			return err
		}
	}
	return nil
}

func absorbPendingRanking(ctx context.Context, db *store.Store) error {
	attempts, err := db.ListAttempts(ctx, "")
	if err != nil {
		return err
	}
	for i := range attempts {
		attempt := attempts[i]
		if attempt.Outcome != "" || host.LiveHost(attempt) {
			continue
		}
		if attempt.EndedAt.IsZero() && attempt.Exit == nil {
			continue
		}
		card, err := db.GetCard(ctx, attempt.Card)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return err
		}
		if card.Kind != model.KindRanking {
			continue
		}
		text, err := os.ReadFile(filepath.Join(attempt.Worktree, ".cardex-result.md"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if err := AbsorbRanking(ctx, Deps{Store: db}, card, string(text)); err != nil {
			return err
		}
	}
	return nil
}
