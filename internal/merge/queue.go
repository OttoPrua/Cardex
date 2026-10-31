package merge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"cardex/internal/model"
	"cardex/internal/store"
	"cardex/internal/workspace"
)

// Service runs one objective's merge queue. New binds it to an open store.
type Service struct {
	DB    *store.Store
	locks sync.Map // objective id → *sync.Mutex
}

var _ Queue = (*Service)(nil)

// New returns a queue that reads cards and projects from db.
func New(db *store.Store) *Service {
	return &Service{DB: db}
}

func (q *Service) mutex(objectiveID string) *sync.Mutex {
	v, _ := q.locks.LoadOrStore(objectiveID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// Enqueue marks the card merge_queued. A card already queued or merged stays put.
func (q *Service) Enqueue(ctx context.Context, cardID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	card, err := q.DB.GetCard(ctx, cardID)
	if err != nil {
		return err
	}
	if card.Status == model.CardMergeQueued || card.Status == model.CardMerged {
		return nil
	}
	card.Status = model.CardMergeQueued
	card.WaitReason = ""
	return q.DB.UpdateCard(ctx, &card)
}

// Step merges the oldest queued card of one objective.
// A second call for the same objective blocks until the first returns, then
// merges the next card onto the integration HEAD the first call produced.
// A different objective uses a different lock.
func (q *Service) Step(ctx context.Context, objectiveID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mu := q.mutex(objectiveID)
	mu.Lock()
	defer mu.Unlock()
	cards, err := q.DB.ListCards(ctx, objectiveID)
	if err != nil {
		return err
	}
	var card *model.Card
	for i := range cards {
		if cards[i].Status == model.CardMergeQueued {
			card = &cards[i]
			break
		}
	}
	if card == nil {
		return nil
	}
	return q.mergeOne(ctx, card)
}

func (q *Service) mergeOne(ctx context.Context, card *model.Card) error {
	obj, err := q.DB.GetObjective(ctx, card.Objective)
	if err != nil {
		return err
	}
	project, err := q.DB.GetProject(ctx, obj.Project)
	if err != nil {
		return err
	}
	repo := project.RepoPath
	branch := obj.IntegrationBranch
	if strings.TrimSpace(repo) == "" || strings.TrimSpace(branch) == "" {
		return fmt.Errorf("merge: objective %s has no repo or integration branch", obj.ID)
	}
	cardBranch := CardBranch(obj.ID, subjectID(card))
	oldHead, err := gitOut(ctx, repo, "rev-parse", branch)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "cardex-merge-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	wt := filepath.Join(tmp, "wt")
	if _, err := gitOut(ctx, repo, "worktree", "add", "--detach", wt, oldHead); err != nil {
		return err
	}
	defer func() {
		_, _ = gitOut(context.Background(), repo, "worktree", "remove", "--force", wt)
	}()

	mergeOut, mergeErr := gitCombined(ctx, wt, mergeArgs(cardBranch)...)
	if mergeErr != nil {
		report, uerr := conflictReport(wt)
		if uerr == nil && report != "" {
			_, _ = gitOut(ctx, wt, "merge", "--abort")
			return q.failCard(ctx, card, model.KindRebase, "rb-", report)
		}
		return fmt.Errorf("merge %s: %w: %s", card.ID, mergeErr, strings.TrimSpace(string(mergeOut)))
	}
	if out, failed := runChecks(ctx, wt, card.Acceptance, project.TestCmd); failed {
		return q.failCard(ctx, card, model.KindRepair, "rp-", out)
	}
	newHead, err := gitOut(ctx, wt, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if newHead != oldHead {
		if _, err := gitOut(ctx, repo, "update-ref", "refs/heads/"+branch, newHead, oldHead); err != nil {
			return err
		}
	}
	if err := q.removeWorktrees(ctx, card); err != nil {
		return err
	}
	fresh, err := q.DB.GetCard(ctx, card.ID)
	if err != nil {
		return err
	}
	fresh.Status = model.CardMerged
	fresh.WaitReason = ""
	if err := q.DB.UpdateCard(ctx, &fresh); err != nil {
		return err
	}
	if fresh.Kind == model.KindRebase || fresh.Kind == model.KindRepair {
		if len(fresh.Deps) > 0 {
			parent, err := q.DB.GetCard(ctx, fresh.Deps[0])
			if err == nil && parent.Status != model.CardMerged {
				parent.Status = model.CardMerged
				parent.WaitReason = ""
				_ = q.DB.UpdateCard(ctx, &parent)
				_ = q.removeWorktrees(ctx, &parent)
			}
		}
	}
	return nil
}

func subjectID(card *model.Card) string {
	if (card.Kind == model.KindRebase || card.Kind == model.KindRepair) && len(card.Deps) > 0 {
		return card.Deps[0]
	}
	return card.ID
}

// CardBranch is the branch a card's attempts commit to.
func CardBranch(objectiveID, cardID string) string {
	return "cardex/" + objectiveID + "/c/" + cardID
}

func mergeArgs(branch string) []string {
	return []string{
		"-c", "user.name=cardex",
		"-c", "user.email=cardex@cardex.invalid",
		"-c", "commit.gpgsign=false",
		"merge", "--no-edit", branch,
	}
}

func (q *Service) failCard(ctx context.Context, card *model.Card, kind model.CardKind, prefix, detail string) error {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = string(kind) + " failed"
	}
	id := prefix + card.ID
	if _, err := q.DB.GetCard(ctx, id); err != nil {
		follow := model.Card{
			ID:         id,
			Objective:  card.Objective,
			Node:       card.Node,
			Kind:       kind,
			Title:      string(kind) + " " + card.Title,
			Body:       detail,
			Tier:       model.TierT2,
			Status:     model.CardReady,
			Deps:       []string{subjectID(card)},
			Base:       CardBranch(card.Objective, subjectID(card)),
			Acceptance: append([]string(nil), card.Acceptance...),
			ReworkNote: detail,
		}
		if err := q.DB.CreateCard(ctx, &follow); err != nil {
			return err
		}
	}
	fresh, err := q.DB.GetCard(ctx, card.ID)
	if err != nil {
		return err
	}
	if fresh.Status != model.CardMerged {
		fresh.Status = model.CardWaiting
		fresh.WaitReason = string(kind) + " " + id
		if err := q.DB.UpdateCard(ctx, &fresh); err != nil {
			return err
		}
	}
	return nil
}

func (q *Service) removeWorktrees(ctx context.Context, card *model.Card) error {
	attempts, err := q.DB.ListAttempts(ctx, card.ID)
	if err != nil {
		return err
	}
	var git workspace.Git
	for _, attempt := range attempts {
		path := strings.TrimSpace(attempt.Worktree)
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if err := git.Remove(ctx, path); err != nil {
			return err
		}
	}
	return nil
}

func conflictReport(dir string) (string, error) {
	raw, err := gitOut(context.Background(), dir, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return "", err
	}
	names := splitLines(raw)
	if len(names) == 0 {
		return "", nil
	}
	var b strings.Builder
	for _, name := range names {
		fmt.Fprintf(&b, "file: %s\n", name)
		body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			fmt.Fprintf(&b, "unreadable: %s\n", err)
			continue
		}
		hunks := conflictHunks(string(body))
		if hunks == "" {
			b.WriteString("(no conflict markers)\n")
			continue
		}
		b.WriteString(hunks)
		if !strings.HasSuffix(hunks, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String(), nil
}

func conflictHunks(body string) string {
	var b strings.Builder
	lines := strings.Split(body, "\n")
	on := false
	for _, line := range lines {
		if strings.HasPrefix(line, "<<<<<<<") {
			on = true
		}
		if on {
			b.WriteString(line)
			b.WriteByte('\n')
		}
		if strings.HasPrefix(line, ">>>>>>>") {
			on = false
		}
	}
	return b.String()
}

func runChecks(ctx context.Context, dir string, acceptance []string, testCmd string) (string, bool) {
	var all []string
	all = append(all, acceptance...)
	if strings.TrimSpace(testCmd) != "" {
		all = append(all, testCmd)
	}
	var buf bytes.Buffer
	for _, command := range all {
		command = strings.TrimSpace(command)
		if command == "" {
			continue
		}
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		fmt.Fprintf(&buf, "$ %s\n%s", command, out)
		if err != nil {
			fmt.Fprintf(&buf, "exit: %v\n", err)
			return buf.String(), true
		}
	}
	return buf.String(), false
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitCombined(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitCombined(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=cardex",
		"GIT_AUTHOR_EMAIL=cardex@cardex.invalid",
		"GIT_COMMITTER_NAME=cardex",
		"GIT_COMMITTER_EMAIL=cardex@cardex.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return out, nil
}

func splitLines(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
