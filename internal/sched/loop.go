package sched

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cardex/internal/claims"
	"cardex/internal/classify"
	"cardex/internal/harvest"
	"cardex/internal/host"
	"cardex/internal/loop"
	"cardex/internal/merge"
	"cardex/internal/model"
	"cardex/internal/notify"
	"cardex/internal/prompt"
	"cardex/internal/question"
	"cardex/internal/quota"
	"cardex/internal/route"
	"cardex/internal/store"
	"cardex/internal/workspace"
)

const (
	fillWindow = 2 * time.Hour
	fillMargin = 0.20
	hardDur    = "30m"
)

// Collaborators the default path calls. Tests replace them to observe the call.
var (
	designStep  = loop.DesignStep
	devStep     = loop.DevStep
	buildPrompt = prompt.Build
	deliverMsg  = func(ctx context.Context, msg notify.Message) error {
		return notify.Stub{}.Deliver(ctx, msg)
	}
	startHost   = launchDetached
	chooseRoute = route.Select
)

// Runner is one scheduler pass over a CARDEX_HOME.
// A nil Dispose is an error and leaves the harvest unconsumed.
type Runner struct {
	Home   string
	Now    func() time.Time
	Engine func(ctx context.Context, objectiveID string) error
	Prompt func(ctx context.Context, in prompt.Input) (string, error)
	Notify func(ctx context.Context, msg notify.Message) error
	Launch func(ctx context.Context, home, attemptID string) (int, error)
	Script func(card model.Card) ([]byte, bool)
	// Dispose receives each harvested result. New installs loop.Dispose.
	// A nil hook returns an error and leaves the attempt unconsumed.
	// A hook error leaves the attempt unconsumed and does not change the card.
	// Sched does not create a review, enqueue a merge, or read pass/rework.
	Dispose func(ctx context.Context, card *model.Card, res harvest.Result) error
	// rankAnswer runs after Questions stores an answer. New approves 批准.
	rankAnswer func(ctx context.Context, db *store.Store, q model.Question) error
	// Classify is the §7.4 single call. New installs productionClassify.
	// A nil call persists the kind default.
	Classify classify.Call
	// mail is the production deliverer. Design questions and notify share it.
	mail notify.Deliverer
	// tickMu guards tickAt, the in-memory tick_error throttle.
	tickMu sync.Mutex
	tickAt map[string]time.Time
}

// New returns the production runner for home with loop.Dispose installed.
func New(home string) *Runner {
	r := &Runner{Home: home}
	r.Dispose = func(ctx context.Context, card *model.Card, res harvest.Result) error {
		return loop.Dispose(loop.WithHome(ctx, home), card, res)
	}
	r.rankAnswer = func(ctx context.Context, db *store.Store, q model.Question) error {
		if strings.TrimSpace(q.Answer) != "批准" {
			return nil
		}
		return loop.FollowRanking(loop.WithHome(ctx, home), db)
	}
	r.ensureMail()
	// DesignStep stays ErrNotImplemented: that export and its test sit outside
	// this lane. The default engine calls loop.New(...).Step directly, then DevStep.
	r.Engine = r.productionEngine
	r.Classify = r.productionClassify
	return r
}

// Questions returns the question service for db with the hook New registered.
// An unhooked question.New only stores the answer.
func (r *Runner) Questions(db *store.Store) question.Service {
	svc := question.New(db, nil)
	if r != nil && r.rankAnswer != nil {
		question.AfterAnswer(svc, func(ctx context.Context, q model.Question) error {
			return r.rankAnswer(ctx, db, q)
		})
	}
	return svc
}

func (r *Runner) now() time.Time {
	if r != nil && r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Tick runs design §6.1 once. A second call with no new facts does not
// launch, merge, schedule, or notify the same work again.
// A step error is recorded and later steps still run. The first of those
// errors is returned after the round. A canceled context stops the round.
func (r *Runner) Tick(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var first error
	keep := func(err error) bool {
		if err == nil {
			return false
		}
		if ctx.Err() != nil {
			return true
		}
		if first == nil {
			first = err
		}
		return false
	}
	var reconcileErr error
	if err := host.Reconcile(ctx, r.Home); err != nil {
		if keep(err) {
			return ctx.Err()
		}
		reconcileErr = err
	}
	db, err := store.Open(filepath.Join(r.Home, "cardex.db"))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if reconcileErr != nil {
			fmt.Fprintf(os.Stderr, "tick_error step=%s object=%s error=%s\n", "reconcile", "tick", clipSummary(reconcileErr.Error()))
		}
		fmt.Fprintf(os.Stderr, "tick_error step=%s object=%s error=%s\n", "store", "tick", clipSummary(err.Error()))
		if first == nil {
			first = err
		}
		return first
	}
	defer db.Close()
	if reconcileErr != nil {
		r.noteTickError(ctx, db, "reconcile", "", "", reconcileErr)
	}
	if err := r.harvest(ctx, db); err != nil {
		if keep(err) {
			return ctx.Err()
		}
		if !errors.As(err, &tickNoted{}) {
			r.noteTickError(ctx, db, "harvest", "", "", err)
		}
	}
	if err := r.advance(ctx, db); err != nil {
		if keep(err) {
			return ctx.Err()
		}
		if !errors.As(err, &tickNoted{}) {
			r.noteTickError(ctx, db, "advance", "", "", err)
		}
	}
	if err := r.mergeStep(ctx, db); err != nil {
		if keep(err) {
			return ctx.Err()
		}
		if !errors.As(err, &tickNoted{}) {
			r.noteTickError(ctx, db, "merge", "", "", err)
		}
	}
	// A stored 批准 from question.New is applied here, before dispatch, so this tick routes with the new table.
	if err := loop.FollowRanking(loop.WithHome(ctx, r.Home), db); err != nil {
		if keep(err) {
			return ctx.Err()
		}
		r.noteTickError(ctx, db, "ranking", "", "", err)
	}
	if _, err := r.dispatch(ctx, db); err != nil {
		if keep(err) {
			return ctx.Err()
		}
		if !errors.As(err, &tickNoted{}) {
			r.noteTickError(ctx, db, "dispatch", "", "", err)
		}
	}
	if err := r.schedules(ctx, db); err != nil {
		if keep(err) {
			return ctx.Err()
		}
		r.noteTickError(ctx, db, "schedules", "", "", err)
	}
	if err := r.notify(ctx, db); err != nil {
		if keep(err) {
			return ctx.Err()
		}
		r.noteTickError(ctx, db, "notify", "", "", err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return first
}

func (r *Runner) harvest(ctx context.Context, db *store.Store) error {
	attempts, err := db.ListAttempts(ctx, "")
	if err != nil {
		return err
	}
	var first error
	for i := range attempts {
		attempt := attempts[i]
		if attempt.Outcome != "" || host.LiveHost(attempt) {
			continue
		}
		if attempt.EndedAt.IsZero() && attempt.Exit == nil {
			continue
		}
		// Only the attempt the classification call created is exempt.
		// A user card whose id starts with cls- stays on this path.
		classifying, err := classificationAttempt(ctx, db, attempt.ID)
		if err != nil {
			if err := r.keepStep(ctx, db, "harvest", "card", attempt.Card, err, &first); err != nil {
				return err
			}
			continue
		}
		if classifying {
			if attempt.Outcome == "" {
				attempt.Outcome = model.OutcomeNoOutput
				pctx, cancel := classifyPersistContext(ctx)
				_ = db.UpdateAttempt(pctx, &attempt)
				cancel()
			}
			continue
		}
		card, err := db.GetCard(ctx, attempt.Card)
		if err != nil {
			if err := r.keepStep(ctx, db, "harvest", "card", attempt.Card, err, &first); err != nil {
				return err
			}
			continue
		}
		if info, statErr := os.Stat(attempt.Worktree); statErr != nil || !info.IsDir() {
			excerpt := attempt.ErrorExcerpt
			if excerpt == "" {
				excerpt = "host lost"
			}
			res := harvest.Result{Outcome: model.OutcomeNoOutput, Excerpt: excerpt}
			if err := r.deliver(ctx, db, &card, &attempt, res); err != nil {
				if err := r.keepStep(ctx, db, "harvest", "card", attempt.Card, err, &first); err != nil {
					return err
				}
			}
			continue
		}
		output, _ := os.ReadFile(filepath.Join(r.Home, "attempts", attempt.ID, "output.log"))
		exitCode := 1
		if attempt.Exit != nil {
			exitCode = *attempt.Exit
		}
		res, err := harvest.Harvest(ctx, harvest.Input{
			Attempt:    attempt,
			Card:       card,
			Exit:       exitCode,
			Output:     output,
			ErrorClass: attempt.ErrorClass,
			Repo:       attempt.Worktree,
		})
		if err != nil {
			if err := r.keepStep(ctx, db, "harvest", "card", attempt.Card, err, &first); err != nil {
				return err
			}
			continue
		}
		if fam := r.familyOf(ctx, db, attempt.Route.Channel); fam != "" && card.AuthorFamily == "" {
			card.AuthorFamily = fam
		}
		var kept []model.Claim
		for _, claim := range res.Claims {
			if strings.TrimSpace(claim.Path) == "" {
				continue
			}
			kept = append(kept, claim)
		}
		if len(kept) > 0 {
			card.Claims = kept
		}
		if err := r.deliver(ctx, db, &card, &attempt, res); err != nil {
			if err := r.keepStep(ctx, db, "harvest", "card", attempt.Card, err, &first); err != nil {
				return err
			}
		}
	}
	return first
}

// deliver hands the harvested result to the disposition collaborator.
// The attempt is stored as consumed, and the card is written, only after
// that call returns nil. A nil collaborator is an error and changes nothing.
// Sched does not turn a passed dev into a review, enqueue the reviewed card,
// or scan the body for pass/rework.
func (r *Runner) deliver(ctx context.Context, db *store.Store, card *model.Card, attempt *model.Attempt, res harvest.Result) error {
	if r == nil || r.Dispose == nil {
		return fmt.Errorf("sched: harvest disposition is not installed")
	}
	ctx = loop.WithStore(loop.WithAttempt(ctx, attempt.ID), db)
	if err := r.Dispose(ctx, card, res); err != nil {
		return err
	}
	attempt.Outcome = res.Outcome
	if res.ErrorClass != "" {
		attempt.ErrorClass = res.ErrorClass
	}
	if attempt.ErrorExcerpt == "" {
		attempt.ErrorExcerpt = res.Excerpt
	}
	if err := db.UpdateCard(ctx, card); err != nil {
		return err
	}
	return db.UpdateAttempt(ctx, attempt)
}

func (r *Runner) familyOf(ctx context.Context, db *store.Store, channelID string) string {
	if channelID == "" {
		return ""
	}
	ch, err := db.GetChannel(ctx, channelID)
	if err != nil {
		return ""
	}
	return ch.Family
}

func (r *Runner) advance(ctx context.Context, db *store.Store) error {
	objs, err := db.ListObjectives(ctx, "")
	if err != nil {
		return err
	}
	var first error
	for _, obj := range objs {
		if obj.Mode != model.ModeAuto || obj.Status != model.ObjectiveActive {
			continue
		}
		var stepErr error
		if r.Engine != nil {
			stepErr = r.Engine(ctx, obj.ID)
		} else {
			stepErr = designStep(ctx, obj.ID)
			if stepErr == nil || errors.Is(stepErr, loop.ErrNotImplemented) {
				stepErr = devStep(ctx, obj.ID)
			}
		}
		if stepErr == nil || errors.Is(stepErr, loop.ErrNotImplemented) {
			continue
		}
		if err := r.keepStep(ctx, db, "advance", "objective", obj.ID, stepErr, &first); err != nil {
			return err
		}
	}
	return first
}

func (r *Runner) mergeStep(ctx context.Context, db *store.Store) error {
	objs, err := db.ListObjectives(ctx, "")
	if err != nil {
		return err
	}
	q := merge.New(db)
	var first error
	for _, obj := range objs {
		if obj.Status != model.ObjectiveActive && obj.Status != model.ObjectiveDoneCandidate {
			continue
		}
		if err := q.Step(ctx, obj.ID); err != nil {
			if err := r.keepStep(ctx, db, "merge", "objective", obj.ID, err, &first); err != nil {
				return err
			}
		}
	}
	return first
}

func (r *Runner) dispatch(ctx context.Context, db *store.Store) (map[string]bool, error) {
	if err := releaseDueBackoff(ctx, db, r.now()); err != nil {
		return nil, err
	}
	used := map[string]bool{}
	cards, err := db.ListCards(ctx, "")
	if err != nil {
		return nil, err
	}
	attempts, err := db.ListAttempts(ctx, "")
	if err != nil {
		return nil, err
	}
	live := map[string]bool{}
	inUse := map[string]int{}
	for _, attempt := range attempts {
		if host.LiveHost(attempt) {
			live[attempt.Card] = true
		}
		if attemptOccupies(attempt) {
			inUse[attempt.Route.Channel]++
		}
	}
	objs, err := db.ListObjectives(ctx, "")
	if err != nil {
		return nil, err
	}
	objByID := map[string]model.Objective{}
	for _, obj := range objs {
		objByID[obj.ID] = obj
	}
	var occupy []model.Card
	var ready []model.Card
	for _, card := range cards {
		if card.Status == model.CardRunning || card.Status == model.CardHarvesting {
			occupy = append(occupy, card)
		}
		// Paused, canceled, and other stopped cards stay put even when an
		// open attempt has no PID. A claim wait is still dispatchable.
		if !canDispatch(card) {
			continue
		}
		// A ready backlog waits for fill. One already running with no PID is an
		// interrupted start and resumes here, the same way as any other card.
		if card.Kind == model.KindBacklog && !resumable(card, attempts) {
			continue
		}
		if card.Status == model.CardReady || claimWait(card) || resumable(card, attempts) {
			ready = append(ready, card)
		}
	}
	sortCards(ready, objByID)
	channels, err := db.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	pos, err := db.ListPositioning(ctx)
	if err != nil {
		return nil, err
	}
	quotas, err := r.quotaView(ctx, db, channels)
	if err != nil {
		return nil, err
	}
	for _, card := range ready {
		if live[card.ID] {
			continue
		}
		obj, ok := objByID[card.Objective]
		if !ok {
			continue
		}
		allowed, err := r.allow(ctx, db, &card, obj, occupy, true)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			r.noteTickError(ctx, db, "dispatch", "card", card.ID, err)
			continue
		}
		if !allowed {
			continue
		}
		ch, err := r.launchCard(ctx, db, card, obj, channels, pos, inUse, quotas)
		if err != nil {
			if errors.Is(err, route.ErrNoCandidate) || errors.Is(err, route.ErrPinnedUnavailable) {
				continue
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			r.noteTickError(ctx, db, "dispatch", "card", card.ID, err)
			continue
		}
		if ch != "" {
			used[ch] = true
		}
		live[card.ID] = true
		if card.Kind != model.KindReview && len(card.Claims) > 0 {
			card.Status = model.CardRunning
			occupy = append(occupy, card)
		}
	}
	if err := r.fill(ctx, db, used, cards, objByID, channels, pos, occupy, inUse, quotas); err != nil {
		return nil, err
	}
	return used, nil
}

func claimWait(card model.Card) bool {
	return card.Status == model.CardWaiting && strings.Contains(card.WaitReason, "占用")
}

func conflictReason(conflicts []claims.Conflict, cardID string) string {
	for _, item := range conflicts {
		if item.Card == cardID {
			return item.Reason
		}
	}
	return ""
}

func sortCards(cards []model.Card, objs map[string]model.Objective) {
	for i := 1; i < len(cards); i++ {
		j := i
		for j > 0 && lessCard(cards[j], cards[j-1], objs) {
			cards[j], cards[j-1] = cards[j-1], cards[j]
			j--
		}
	}
}

func lessCard(a, b model.Card, objs map[string]model.Objective) bool {
	pa, pb := objs[a.Objective].Priority, objs[b.Objective].Priority
	if pa != pb {
		return pa > pb
	}
	return a.ID < b.ID
}

func (r *Runner) launchCard(ctx context.Context, db *store.Store, card model.Card, obj model.Objective, channels []model.Channel, pos []model.Positioning, inUse map[string]int, quotas route.Quotas) (string, error) {
	if open, err := openAttempt(ctx, db, card.ID); err != nil {
		return "", err
	} else if open != nil {
		if !canDispatch(card) {
			return "", nil
		}
		return r.resumeAttempt(ctx, db, card, obj, open)
	}
	project, err := db.GetProject(ctx, obj.Project)
	if err != nil {
		return "", err
	}
	prev, err := db.ListAttempts(ctx, card.ID)
	if err != nil {
		return "", err
	}
	card, classified, err := r.classifyBeforeRoute(ctx, db, card)
	if err != nil {
		return "", err
	}
	// dispatch and fill ignore ErrNoCandidate and ErrPinnedUnavailable.
	// A stored channel that is missing, not ok, or blocked until a future reset
	// is released on this request only. The objective row is not written.
	rt, reason, rel, settled, err := r.chooseLaunchRoute(ctx, db, card, obj, channels, pos, inUse, quotas, classified)
	if err != nil {
		return "", err
	}
	if settled {
		return "", nil
	}
	// A script-less fake exits before it can write a design result, and §5
	// then opens a question. Injected Launch stubs still start.
	if r.holdFakeDesign(ctx, db, card, rt, channels) {
		return "", nil
	}
	if rel.released {
		card.Channel = rt.Channel
		card.Model = rt.Model
	}
	attemptID, err := newID("a")
	if err != nil {
		return "", err
	}
	wsCard, cont, readOnly, err := r.workspaceTarget(ctx, db, card)
	if err != nil {
		return "", err
	}
	// The branch and worktree can exist before the attempt row is stored.
	// Continue that branch instead of creating it again.
	if !cont && gitRefExists(ctx, project.RepoPath, "refs/heads/"+cardBranchName(obj.ID, wsCard)) {
		cont = true
	}
	existed := cont
	root := project.WorktreeRoot
	if strings.TrimSpace(root) == "" {
		root = filepath.Join(r.Home, "worktrees")
	}
	baseSHA, err := workspace.Git{}.EnsureIntegrationBranch(ctx, project.RepoPath, obj.IntegrationBranch)
	if err != nil {
		return r.failLaunch(ctx, db, &card, nil, launchStepWorktree, err, launchScratch{})
	}
	wt, err := workspace.Git{}.Create(ctx, workspace.CreateRequest{
		CardID:            wsCard,
		AttemptID:         attemptID,
		ObjectiveID:       obj.ID,
		RepoPath:          project.RepoPath,
		WorktreeRoot:      root,
		IntegrationBranch: obj.IntegrationBranch,
		Continue:          cont,
	})
	scratch := launchScratch{
		repo:         project.RepoPath,
		worktree:     filepath.Join(root, wsCard+"-"+attemptID),
		branch:       cardBranchName(obj.ID, wsCard),
		base:         baseSHA,
		dropWorktree: true,
		dropBranch:   !existed,
	}
	if err != nil {
		return r.failLaunch(ctx, db, &card, nil, launchStepWorktree, err, scratch)
	}
	scratch.worktree = wt.Path
	scratch.branch = wt.Branch
	scratch.base = wt.BaseCommit
	if readOnly {
		if err := detachHead(ctx, wt.Path); err != nil {
			return r.failLaunch(ctx, db, &card, nil, launchStepWorktree, err, scratch)
		}
	}
	dir := filepath.Join(r.Home, "attempts", attemptID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return r.failLaunch(ctx, db, &card, nil, launchStepPrompt, err, scratch)
	}
	promptPath := filepath.Join(dir, "prompt.txt")
	attempt := model.Attempt{
		ID:          attemptID,
		Card:        card.ID,
		Index:       len(prev),
		Route:       rt,
		RouteReason: reason,
		Worktree:    wt.Path,
		Branch:      wt.Branch,
		BaseCommit:  wt.BaseCommit,
		PromptPath:  promptPath,
		StartedAt:   r.now().UTC(),
	}
	if err := db.CreateAttempt(ctx, &attempt); err != nil {
		return r.failLaunch(ctx, db, &card, nil, launchStepPrompt, err, scratch)
	}
	card.Status = model.CardRunning
	card.WaitReason = ""
	if err := db.UpdateCard(ctx, &card); err != nil {
		return r.failLaunch(ctx, db, &card, &attempt, launchStepPrompt, err, scratch)
	}
	if rel.released {
		if err := appendPinRelease(ctx, db, r.now(), card.ID, rel, rt); err != nil {
			return "", err
		}
	}
	if err := r.writePrompt(ctx, db, card, obj, project.RepoPath, &attempt); err != nil {
		return r.failLaunch(ctx, db, &card, &attempt, launchStepPrompt, err, scratch)
	}
	if err := r.startAttempt(ctx, db, &attempt); err != nil {
		return r.failLaunch(ctx, db, &card, &attempt, launchStepHost, err, scratch)
	}
	if inUse != nil && rt.Channel != "" {
		inUse[rt.Channel]++
	}
	return rt.Channel, nil
}

func (r *Runner) resumeAttempt(ctx context.Context, db *store.Store, card model.Card, obj model.Objective, attempt *model.Attempt) (string, error) {
	if !canDispatch(card) {
		return "", nil
	}
	project, err := db.GetProject(ctx, obj.Project)
	if err != nil {
		return "", err
	}
	scratch := launchScratch{
		repo:     project.RepoPath,
		worktree: attempt.Worktree,
		branch:   attempt.Branch,
		base:     attempt.BaseCommit,
	}
	if card.Status != model.CardRunning {
		card.Status = model.CardRunning
		card.WaitReason = ""
		if err := db.UpdateCard(ctx, &card); err != nil {
			return r.failLaunch(ctx, db, &card, attempt, launchStepPrompt, err, scratch)
		}
	}
	if err := r.writePrompt(ctx, db, card, obj, project.RepoPath, attempt); err != nil {
		return r.failLaunch(ctx, db, &card, attempt, launchStepPrompt, err, scratch)
	}
	if err := r.startAttempt(ctx, db, attempt); err != nil {
		return r.failLaunch(ctx, db, &card, attempt, launchStepHost, err, scratch)
	}
	return attempt.Route.Channel, nil
}

func (r *Runner) writePrompt(ctx context.Context, db *store.Store, card model.Card, obj model.Objective, repo string, attempt *model.Attempt) error {
	dir := filepath.Join(r.Home, "attempts", attempt.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Keep the card body's ## 事实摘要, including a direction-node Q0 that is
	// not this node's question. Open questions do not replace that summary.
	in := prompt.Input{
		Card:       card,
		Objective:  obj,
		ReworkNote: card.ReworkNote,
		Repo:       repo,
		Revision:   attempt.BaseCommit,
	}
	if card.Kind == model.KindRanking {
		material, err := loop.RankingMaterial(r.Home)
		if err != nil {
			return err
		}
		in.ReworkNote = material
	}
	text, err := r.promptText(ctx, in)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		text = strings.TrimSpace(card.Title + "\n" + card.Body)
	}
	text = stripEmptyFacts(text)
	promptPath := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte(text), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "hard-duration"), []byte(hardDur), 0o644); err != nil {
		return err
	}
	if script := r.scriptFor(card); len(script) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "script.json"), script, 0o644); err != nil {
			return err
		}
	}
	if attempt.PromptPath != promptPath {
		attempt.PromptPath = promptPath
		if err := db.UpdateAttempt(ctx, attempt); err != nil {
			return err
		}
	}
	return nil
}

// stripEmptyFacts drops a facts section that Build left empty.
// Design, plan, and replan keep the card body's ## 事实摘要.
func stripEmptyFacts(text string) string {
	const start = "<!-- cardex-facts -->\n"
	const end = "<!-- /cardex-facts -->\n"
	for {
		i := strings.Index(text, start)
		if i < 0 {
			return text
		}
		rest := text[i+len(start):]
		j := strings.Index(rest, end)
		if j < 0 {
			return text
		}
		if strings.TrimSpace(rest[:j]) != "" {
			return text
		}
		text = text[:i] + rest[j+len(end):]
	}
}

func (r *Runner) startAttempt(ctx context.Context, db *store.Store, attempt *model.Attempt) error {
	launch := startHost
	if r.Launch != nil {
		launch = func(home, id string) (int, error) { return r.Launch(ctx, home, id) }
	}
	pid, err := launch(r.Home, attempt.ID)
	if err != nil {
		return err
	}
	waitPID(ctx, func() (int, bool) {
		got, err := db.GetAttempt(ctx, attempt.ID)
		if err != nil {
			return 0, false
		}
		if got.PID > 0 || !got.EndedAt.IsZero() {
			return got.PID, true
		}
		return 0, false
	})
	got, err := db.GetAttempt(ctx, attempt.ID)
	if err == nil && got.PID == 0 && got.EndedAt.IsZero() {
		got.PID = pid
		_ = db.UpdateAttempt(ctx, &got)
	}
	return nil
}

func (r *Runner) promptText(ctx context.Context, in prompt.Input) (string, error) {
	if r.Prompt != nil {
		return r.Prompt(ctx, in)
	}
	return buildPrompt(ctx, in)
}

// holdFakeDesign keeps a script-less fake card ready when starting it would
// let §5 open a question. Production Serve leaves Launch nil, so design-family
// and review cards wait until a script is planted. An injected Launch is a
// stub that only records the attempt, so those cards start. The inventory
// research card stays ready even when Launch is wrapped: the wrapper starts a
// real host, and a script-less fake still has no result.
func (r *Runner) holdFakeDesign(ctx context.Context, db *store.Store, card model.Card, rt model.Route, channels []model.Channel) bool {
	if r == nil || !holdableFakeKind(card.Kind) {
		return false
	}
	if r.Launch != nil && !inventoryResearch(card) {
		return false
	}
	if len(r.scriptFor(card)) > 0 {
		return false
	}
	for _, ch := range channels {
		if ch.ID == rt.Channel {
			return ch.Runner == model.RunnerFake
		}
	}
	ch, err := db.GetChannel(ctx, rt.Channel)
	if err != nil {
		return true
	}
	return ch.Runner == model.RunnerFake
}

func holdableFakeKind(kind model.CardKind) bool {
	return designFamilyKind(kind) || kind == model.KindReview
}

func inventoryResearch(card model.Card) bool {
	if card.Kind != model.KindResearch || card.Node != "" {
		return false
	}
	return strings.Contains(card.Title, "盘点") || strings.Contains(card.Body, "Q0")
}

func (r *Runner) scriptFor(card model.Card) []byte {
	if r.Script != nil {
		if b, ok := r.Script(card); ok {
			return b
		}
	}
	body := strings.TrimSpace(card.Body)
	if strings.HasPrefix(body, "{") && strings.Contains(body, `"attempts"`) {
		return []byte(body)
	}
	path := filepath.Join(r.Home, "scripts", card.ID+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return b
}

func (r *Runner) fill(ctx context.Context, db *store.Store, used map[string]bool, cards []model.Card, objByID map[string]model.Objective, channels []model.Channel, pos []model.Positioning, occupy []model.Card, inUse map[string]int, quotas route.Quotas) error {
	var backlog []model.Card
	for _, card := range cards {
		if card.Status == model.CardReady && card.Kind == model.KindBacklog {
			backlog = append(backlog, card)
		}
	}
	if len(backlog) == 0 {
		return nil
	}
	now := r.now()
	for _, ch := range channels {
		est, err := quotas.Remaining(ctx, ch.ID)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			r.noteTickError(ctx, db, "dispatch", "card", "", err)
			continue
		}
		if used[ch.ID] || !windowFills(ch, est, now) || readyNeeds(cards, ch.ID) {
			continue
		}
		var card model.Card
		var obj model.Objective
		found := false
		for i := range backlog {
			candidate := backlog[i]
			o, ok := objByID[candidate.Objective]
			if !ok {
				continue
			}
			allowed, err := r.allow(ctx, db, &candidate, o, occupy, false)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				r.noteTickError(ctx, db, "dispatch", "card", candidate.ID, err)
				continue
			}
			if !allowed {
				continue
			}
			card, obj, found = candidate, o, true
			break
		}
		if !found {
			continue
		}
		// One channel, not an explicit pin: Select still applies health, capability, concurrency, and redline.
		if _, err := r.launchCard(ctx, db, card, obj, []model.Channel{ch}, pos, inUse, quotas); err != nil {
			if errors.Is(err, route.ErrNoCandidate) || errors.Is(err, route.ErrPinnedUnavailable) {
				continue
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			r.noteTickError(ctx, db, "dispatch", "card", card.ID, err)
			continue
		}
		for i := range backlog {
			if backlog[i].ID == card.ID {
				backlog = append(backlog[:i], backlog[i+1:]...)
				break
			}
		}
		used[ch.ID] = true
		if len(card.Claims) > 0 {
			card.Status = model.CardRunning
			occupy = append(occupy, card)
		}
	}
	return nil
}

func readyNeeds(cards []model.Card, channelID string) bool {
	for _, card := range cards {
		if card.Status != model.CardReady || card.Kind == model.KindBacklog {
			continue
		}
		if card.Channel == "" || card.Channel == channelID {
			return true
		}
	}
	return false
}

// windowFills is §7.5. One confirmable estimate has to meet every condition
// together: a configured token or request limit, remaining ratio above the
// redline plus the fill margin, and a reset inside the fill horizon.
// An uncapped window is unknown. A stored Remaining is not that ratio, and
// a near reset cannot borrow another window's ratio.
func windowFills(ch model.Channel, est quota.Estimate, now time.Time) bool {
	if est.Blocked && !est.Reset.IsZero() && now.Before(est.Reset) {
		return false
	}
	if est.Unknown || (est.LimitTokens <= 0 && est.LimitRequests <= 0) {
		return false
	}
	if est.Ratio <= ch.Redline+fillMargin {
		return false
	}
	return resetSoon(est.Reset, now)
}

func resetSoon(reset, now time.Time) bool {
	return !reset.IsZero() && reset.After(now) && reset.Sub(now) <= fillWindow
}

type scheduleFile struct {
	Schedules []scheduleEntry `json:"schedules"`
}

type scheduleEntry struct {
	Name     string       `json:"name"`
	Every    string       `json:"every"`
	Cron     string       `json:"cron"`
	CronLite string       `json:"cron-lite"`
	Template cardTemplate `json:"card_template"`
}

type cardTemplate struct {
	Objective  string   `json:"objective"`
	Kind       string   `json:"kind"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Tier       string   `json:"tier"`
	Acceptance []string `json:"acceptance"`
	Requires   []string `json:"requires"`
}

func (r *Runner) schedules(ctx context.Context, db *store.Store) error {
	raw, err := os.ReadFile(filepath.Join(r.Home, "config.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return loop.ApplyDefaultSchedule(ctx, db, r.Home, r.now())
		}
		return err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil
	}
	if _, set := top["schedules"]; !set {
		return loop.ApplyDefaultSchedule(ctx, db, r.Home, r.now())
	}
	var file scheduleFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil
	}
	now := r.now()
	objs, err := db.ListObjectives(ctx, "")
	if err != nil {
		return err
	}
	for _, entry := range file.Schedules {
		if strings.TrimSpace(entry.Name) == "" {
			continue
		}
		due, slot, ok := scheduleDue(r.Home, entry, now)
		if !ok || !due {
			continue
		}
		if err := enqueueTemplate(ctx, db, objs, entry, slot); err != nil {
			if werr := writeScheduleFault(r.Home, entry.Name, err); werr != nil {
				return werr
			}
			continue
		}
		if err := os.MkdirAll(filepath.Join(r.Home, "schedules"), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(scheduleStamp(r.Home, entry.Name), []byte(slot), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func scheduleDue(home string, entry scheduleEntry, now time.Time) (bool, string, bool) {
	if every := strings.TrimSpace(entry.Every); every != "" {
		dur, err := time.ParseDuration(every)
		if err != nil || dur <= 0 {
			return false, "", false
		}
		secs := int64(dur / time.Second)
		if secs < 1 {
			secs = 1
		}
		slot := strconvSlot(now.Unix() / secs)
		prev, _ := os.ReadFile(scheduleStamp(home, entry.Name))
		if strings.TrimSpace(string(prev)) == slot {
			return false, slot, true
		}
		return true, slot, true
	}
	expr := strings.TrimSpace(entry.Cron)
	if expr == "" {
		expr = strings.TrimSpace(entry.CronLite)
	}
	if expr == "" {
		return false, "", false
	}
	if !cronDue(expr, now) {
		return false, "", true
	}
	slot := now.UTC().Truncate(time.Minute).Format(time.RFC3339)
	prev, _ := os.ReadFile(scheduleStamp(home, entry.Name))
	if strings.TrimSpace(string(prev)) == slot {
		return false, slot, true
	}
	return true, slot, true
}

func strconvSlot(n int64) string {
	return fmt.Sprintf("%d", n)
}

func scheduleStamp(home, name string) string {
	return filepath.Join(home, "schedules", scheduleIdentity(name)+".last")
}

func scheduleIdentity(name string) string {
	return safeName(name) + "-" + nameDigest(name)
}

func nameDigest(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:8])
}

func writeScheduleFault(home, name string, reason error) error {
	if err := os.MkdirAll(filepath.Join(home, "schedules"), 0o755); err != nil {
		return err
	}
	body := "needs review: " + reason.Error() + "\n"
	return os.WriteFile(filepath.Join(home, "schedules", scheduleIdentity(name)+".fault"), []byte(body), 0o644)
}

func enqueueTemplate(ctx context.Context, db *store.Store, objs []model.Objective, entry scheduleEntry, slot string) error {
	tmpl := entry.Template
	objID := strings.TrimSpace(tmpl.Objective)
	if objID == "" {
		for _, obj := range objs {
			if obj.Status == model.ObjectiveActive {
				objID = obj.ID
				break
			}
		}
	}
	if objID == "" {
		return nil
	}
	kind := model.CardKind(strings.TrimSpace(tmpl.Kind))
	if !kind.Valid() || kind == "" {
		kind = model.KindRanking
	}
	tier := model.Tier(strings.TrimSpace(tmpl.Tier))
	if !tier.Valid() || tier == "" {
		tier = model.DefaultTier(kind)
	}
	title := tmpl.Title
	if title == "" {
		title = entry.Name
	}
	id := "sch-" + scheduleIdentity(entry.Name) + "-" + safeName(slot)
	if _, err := db.GetCard(ctx, id); err == nil {
		return nil
	}
	card := model.Card{
		ID: id, Objective: objID, Kind: kind, Title: title, Body: tmpl.Body,
		Tier: tier, Status: model.CardReady, Acceptance: tmpl.Acceptance,
		Requires: append([]string(nil), tmpl.Requires...),
	}
	return db.CreateCard(ctx, &card)
}

func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "job"
	}
	return b.String()
}

func (r *Runner) notify(ctx context.Context, db *store.Store) error {
	questions, err := db.ListQuestions(ctx, "")
	if err != nil {
		return err
	}
	for i := range questions {
		q := questions[i]
		if q.Answer != "" {
			continue
		}
		msg, err := r.deliveryMessage(ctx, db, q)
		if err != nil {
			return err
		}
		if r.Notify != nil {
			err = r.Notify(ctx, msg)
		} else {
			r.ensureMail()
			err = r.mail.Deliver(ctx, msg)
			err = r.handoffDelivery(ctx, db, msg, err)
		}
		if err != nil {
			if errors.Is(err, notify.ErrNotImplemented) {
				continue
			}
			if r.Notify != nil {
				return err
			}
			if noteErr := r.noteDelivery(ctx, db, q, err); noteErr != nil {
				return noteErr
			}
			continue
		}
		// Dedup and backoff live in notify. A prior delivery must not skip the
		// call; record only the first handoff so a quiet tick adds no new row.
		if len(q.Deliveries) > 0 {
			continue
		}
		q.Deliveries = append(q.Deliveries, model.Delivery{
			At:          r.now().UTC(),
			Adapter:     "sched",
			Destination: q.ID,
		})
		if err := db.UpdateQuestion(ctx, &q); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) allow(ctx context.Context, db *store.Store, card *model.Card, obj model.Objective, occupy []model.Card, markWait bool) (bool, error) {
	if obj.Status != model.ObjectiveActive && obj.Status != model.ObjectiveDoneCandidate {
		return false, nil
	}
	if !executionDepsDone(ctx, db, *card) {
		return false, nil
	}
	if card.Kind == model.KindReview {
		return true, nil
	}
	same, err := occupySameRepo(ctx, db, obj, occupy)
	if err != nil {
		return false, err
	}
	probe := make([]model.Card, 0, len(same)+1)
	for _, item := range same {
		if item.ID == card.ID {
			continue
		}
		probe = append(probe, item)
	}
	probe = append(probe, *card)
	conflicts, err := claims.Conflicts(probe, nil)
	if err != nil {
		return false, err
	}
	if reason := conflictReason(conflicts, card.ID); reason != "" {
		if markWait && (card.Status != model.CardWaiting || card.WaitReason != reason) {
			card.Status = model.CardWaiting
			card.WaitReason = reason
			if err := db.UpdateCard(ctx, card); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	return true, nil
}

// occupySameRepo keeps occupants of the dispatching card's repository.
// An empty repo path stays in the set. A card is not dropped for sharing its own id.
func occupySameRepo(ctx context.Context, db *store.Store, obj model.Objective, occupy []model.Card) ([]model.Card, error) {
	repos := map[string]string{}
	mine, err := repoForObjective(ctx, db, obj, repos)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]model.Card, 0, len(occupy))
	for _, item := range occupy {
		if item.ID != "" {
			if seen[item.ID] {
				continue
			}
			seen[item.ID] = true
		}
		other, err := repoForCard(ctx, db, item, repos)
		if err != nil {
			return nil, err
		}
		if mine != "" && other != "" && mine != other {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func repoForObjective(ctx context.Context, db *store.Store, obj model.Objective, cache map[string]string) (string, error) {
	if obj.ID != "" {
		if repo, ok := cache[obj.ID]; ok {
			return repo, nil
		}
	}
	projectID := strings.TrimSpace(obj.Project)
	if projectID == "" && obj.ID != "" {
		got, err := db.GetObjective(ctx, obj.ID)
		if err != nil {
			return "", err
		}
		projectID = strings.TrimSpace(got.Project)
	}
	repo := ""
	if projectID != "" {
		project, err := db.GetProject(ctx, projectID)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(project.RepoPath) != "" {
			repo = filepath.Clean(project.RepoPath)
		}
	}
	if obj.ID != "" {
		cache[obj.ID] = repo
	}
	return repo, nil
}

func repoForCard(ctx context.Context, db *store.Store, card model.Card, cache map[string]string) (string, error) {
	id := strings.TrimSpace(card.Objective)
	if id == "" {
		return "", nil
	}
	if repo, ok := cache[id]; ok {
		return repo, nil
	}
	obj, err := db.GetObjective(ctx, id)
	if err != nil {
		return "", err
	}
	return repoForObjective(ctx, db, obj, cache)
}

func canDispatch(card model.Card) bool {
	switch card.Status {
	case model.CardReady, model.CardRunning:
		return true
	default:
		return claimWait(card)
	}
}

func executionDepsDone(ctx context.Context, db *store.Store, card model.Card) bool {
	skip := ""
	switch card.Kind {
	case model.KindReview, model.KindRepair, model.KindRebase, model.KindNormalize:
		if len(card.Deps) > 0 {
			skip = strings.TrimSpace(card.Deps[0])
		}
	}
	for _, id := range card.Deps {
		id = strings.TrimSpace(id)
		if id == "" || id == skip {
			continue
		}
		dep, err := db.GetCard(ctx, id)
		if err != nil {
			return false
		}
		switch dep.Status {
		case model.CardMerged, model.CardDone, model.CardCanceled:
		default:
			return false
		}
	}
	return true
}

func resumable(card model.Card, attempts []model.Attempt) bool {
	for _, attempt := range attempts {
		if attempt.Card == card.ID && attempt.PID == 0 && attemptOpen(attempt) {
			return true
		}
	}
	return false
}

func attemptOpen(a model.Attempt) bool {
	return a.Outcome == "" && a.EndedAt.IsZero() && a.Exit == nil
}

func attemptOccupies(a model.Attempt) bool {
	return a.Route.Channel != "" && attemptOpen(a)
}

func openAttempt(ctx context.Context, db *store.Store, cardID string) (*model.Attempt, error) {
	attempts, err := db.ListAttempts(ctx, cardID)
	if err != nil {
		return nil, err
	}
	var found *model.Attempt
	for i := range attempts {
		if attempts[i].PID == 0 && attemptOpen(attempts[i]) {
			copy := attempts[i]
			found = &copy
		}
	}
	return found, nil
}

func (r *Runner) workspaceTarget(ctx context.Context, db *store.Store, card model.Card) (string, bool, bool, error) {
	switch card.Kind {
	case model.KindReview:
		// The candidate branch belongs to the original card. A repair or rebase
		// subject does not have a branch under its own id.
		id, err := reviewBranchCard(ctx, db, reviewSubject(card))
		return id, true, true, err
	case model.KindRepair, model.KindRebase:
		id, err := rootCardID(ctx, db, card)
		return id, true, false, err
	default:
		return card.ID, card.ReworkCount > 0, false, nil
	}
}

func reviewBranchCard(ctx context.Context, db *store.Store, subjectID string) (string, error) {
	if subjectID == "" {
		return "", fmt.Errorf("sched: review has no subject")
	}
	subject, err := db.GetCard(ctx, subjectID)
	if err != nil {
		return "", err
	}
	if subject.Kind == model.KindRepair || subject.Kind == model.KindRebase {
		return rootCardID(ctx, db, subject)
	}
	return subject.ID, nil
}

func reviewSubject(card model.Card) string {
	if len(card.Deps) > 0 && strings.TrimSpace(card.Deps[0]) != "" {
		return strings.TrimSpace(card.Deps[0])
	}
	if strings.HasPrefix(card.ID, "rv-") {
		return strings.TrimPrefix(card.ID, "rv-")
	}
	return card.ID
}

func rootCardID(ctx context.Context, db *store.Store, card model.Card) (string, error) {
	cur := card
	seen := map[string]bool{}
	for (cur.Kind == model.KindRepair || cur.Kind == model.KindRebase) && len(cur.Deps) > 0 {
		next := strings.TrimSpace(cur.Deps[0])
		if next == "" || seen[next] {
			break
		}
		seen[next] = true
		parent, err := db.GetCard(ctx, next)
		if err != nil {
			return "", err
		}
		if parent.Kind != model.KindRepair && parent.Kind != model.KindRebase {
			return parent.ID, nil
		}
		cur = parent
	}
	return cur.ID, nil
}

func detachHead(ctx context.Context, worktree string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", worktree, "checkout", "--detach")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("detach review worktree: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// quotaView is the estimator dispatch and fill both pass to routing.
// Limited windows use configured caps, each unreset snapshot on its own window,
// finished-attempt usage counted once, and the still-valid quota-hit reset.
// A window with no token or request limit stays unknown.
func (r *Runner) quotaView(ctx context.Context, db *store.Store, channels []model.Channel) (route.Quotas, error) {
	led := quota.New()
	led.SetClock(func() time.Time { return r.now() })
	now := r.now().UTC()
	attempts, err := db.ListAttempts(ctx, "")
	if err != nil {
		return nil, err
	}
	omitted := map[string]int64{}
	for _, ch := range channels {
		led.Configure(ch)
		n, err := r.seedChannel(ctx, led, ch, attempts, now)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			omitted[ch.ID] = n
		}
	}
	return uncertainView{Ledger: led, omitted: omitted}, nil
}

// uncertainView is the ledger dispatch and fill read.
// A snapshot charge that could not be placed keeps represented usage and
// marks the unexpressed part uncertain instead of a confidence-1 full window.
type uncertainView struct {
	*quota.Ledger
	omitted map[string]int64
}

func (v uncertainView) Remaining(ctx context.Context, channelID string) (quota.Estimate, error) {
	est, err := v.Ledger.Remaining(ctx, channelID)
	if err != nil || v.omitted[channelID] <= 0 || est.Confidence < 1 {
		return est, err
	}
	est.Confidence = 0
	if est.UsedTokens == 0 {
		est.Unknown = true
	}
	return est, nil
}

func (r *Runner) seedChannel(ctx context.Context, led *quota.Ledger, ch model.Channel, attempts []model.Attempt, now time.Time) (int64, error) {
	omitted, err := recordSnapshot(ctx, led, ch, now)
	if err != nil {
		return 0, err
	}
	var hits []quotaHit
	for i := range attempts {
		a := attempts[i]
		if a.Route.Channel != ch.ID || !attemptFinished(a) {
			continue
		}
		usage := a.Usage
		if usage.Tokens > 0 && strings.TrimSpace(usage.Source) == "" {
			usage.Source = "attempt"
		}
		ended := a.EndedAt
		if ended.IsZero() {
			ended = a.StartedAt
		}
		if err := led.Record(ctx, ch.ID, usage, a.StartedAt, ended); err != nil {
			return 0, err
		}
		if a.Outcome != model.OutcomeQuotaHit && a.ErrorClass != model.ErrorQuotaHit {
			continue
		}
		reset, ok := quotaReset(r.Home, a)
		if !ok {
			continue
		}
		observed := a.EndedAt
		if observed.IsZero() {
			observed = a.StartedAt
		}
		hits = append(hits, quotaHit{observed: observed, reset: reset})
	}
	reset, ok := effectiveQuotaBlock(hits, now)
	if !ok {
		return omitted, nil
	}
	if err := led.ObserveLimit(ctx, ch.ID, reset); err != nil {
		return 0, err
	}
	return omitted, nil
}

func attemptFinished(a model.Attempt) bool {
	return a.Outcome != "" || a.Exit != nil || !a.EndedAt.IsZero()
}

func quotaReset(home string, a model.Attempt) (time.Time, bool) {
	if t, ok := findReset(a.ErrorExcerpt); ok {
		return t, true
	}
	body, err := os.ReadFile(filepath.Join(home, "attempts", a.ID, "output.log"))
	if err != nil {
		return time.Time{}, false
	}
	return findReset(string(body))
}

func cardBranchName(objectiveID, cardID string) string {
	return "cardex/" + objectiveID + "/c/" + cardID
}

func gitRefExists(ctx context.Context, repo, ref string) bool {
	if strings.TrimSpace(repo) == "" || strings.TrimSpace(ref) == "" {
		return false
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "show-ref", "--verify", "--quiet", ref)
	return cmd.Run() == nil
}

func newID(prefix string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b[:]), nil
}
