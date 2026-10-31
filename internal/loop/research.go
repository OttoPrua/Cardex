package loop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"cardex/internal/model"
	"cardex/internal/workspace"
)

// commitDiff is the two-commit comparison workspace.Manager does not provide yet.
// from is the attempt base recorded at dispatch; to is the integration branch HEAD.
type commitDiff interface {
	CompareCommits(ctx context.Context, repo, from, to string) (workspace.Diff, error)
}

// integrationHead is the integration-branch HEAD read workspace.Manager does not provide yet.
type integrationHead interface {
	Head(ctx context.Context, repo, ref string) (string, error)
}

func (d Deps) repoPath(ctx context.Context, obj model.Objective) (string, error) {
	if d.Store == nil || obj.Project == "" || d.WS == nil {
		return "", nil
	}
	p, err := d.Store.GetProject(ctx, obj.Project)
	if err != nil {
		return "", err
	}
	return p.RepoPath, nil
}

func (d Deps) dispatchResearch(ctx context.Context, obj model.Objective) error {
	nodes, err := d.Store.ListNodes(ctx, obj.ID)
	if err != nil {
		return err
	}
	cards, err := d.Store.ListCards(ctx, obj.ID)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		if unfinishedResearch(cards, node.ID) {
			continue
		}
		groups := groupFacts(node.OpenQuestions)
		if len(groups) == 0 {
			continue
		}
		for _, g := range groups {
			body := researchBody(g.items)
			card := &model.Card{
				Objective: obj.ID,
				Node:      node.ID,
				Kind:      model.KindResearch,
				Title:     "研究 " + node.ID,
				Body:      body,
				Tier:      g.tier,
				Status:    model.CardReady,
			}
			if g.web {
				card.Requires = []string{"web"}
			}
			if err := d.addCard(ctx, obj, card); err != nil {
				return err
			}
			ids := make([]string, len(g.items))
			for i, q := range g.items {
				ids[i] = q.ID
				for j := range node.OpenQuestions {
					if node.OpenQuestions[j].ID == q.ID {
						node.OpenQuestions[j].Status = model.OQResearching
					}
				}
			}
			raw, err := json.Marshal(ids)
			if err != nil {
				return err
			}
			if err := d.noteEvent(ctx, "card:"+card.ID, evResearch, raw); err != nil {
				return err
			}
		}
		if err := d.Store.UpdateNode(ctx, &node); err != nil {
			return err
		}
		cards, err = d.Store.ListCards(ctx, obj.ID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (d Deps) absorbResearch(ctx context.Context, obj model.Objective, card model.Card, text, base string) error {
	node, err := d.Store.GetNode(ctx, card.Node)
	if err != nil {
		return err
	}
	ids, err := d.researchIDs(ctx, card)
	if err != nil {
		return err
	}
	blocks := parseFactSection(splitSections(text)["事实"])
	for _, id := range ids {
		b, ok := blocks[id]
		if !ok {
			b = factBlock{}
		}
		for i := range node.OpenQuestions {
			if node.OpenQuestions[i].ID != id {
				continue
			}
			applyBlock(&node.OpenQuestions[i], b, base)
		}
	}
	if err := d.Store.UpdateNode(ctx, &node); err != nil {
		return err
	}
	if card.Status != model.CardDone {
		card.Status = model.CardDone
		if err := d.Store.UpdateCard(ctx, &card); err != nil {
			return err
		}
	}
	return d.markAbsorbed(ctx, card.ID, nil)
}

func (d Deps) researchIDs(ctx context.Context, card model.Card) ([]string, error) {
	ev, err := d.Store.ListEvents(ctx, "card:"+card.ID)
	if err != nil {
		return nil, err
	}
	for _, e := range ev {
		if e.Type != evResearch || len(e.Data) == 0 {
			continue
		}
		var ids []string
		if err := json.Unmarshal(e.Data, &ids); err != nil {
			return nil, err
		}
		return ids, nil
	}
	return questionIDsInBody(card.Body), nil
}

func (d Deps) markStale(ctx context.Context, obj model.Objective, n *model.DesignNode) error {
	if d.WS == nil {
		return nil
	}
	heads, headOK := d.WS.(integrationHead)
	diffs, diffOK := d.WS.(commitDiff)
	if !headOK || !diffOK {
		return nil
	}
	repo, err := d.repoPath(ctx, obj)
	if err != nil || repo == "" {
		return err
	}
	head, err := heads.Head(ctx, repo, obj.IntegrationBranch)
	if err != nil || head == "" {
		return err
	}
	cache := map[string]map[string]bool{}
	for i, q := range n.OpenQuestions {
		if q.Type != model.OQFact || q.Commit == "" || len(q.CitedPaths) == 0 {
			continue
		}
		if q.Status != model.OQAnswered && q.Status != model.OQStale {
			continue
		}
		files, ok := cache[q.Commit]
		if !ok {
			diff, err := diffs.CompareCommits(ctx, repo, q.Commit, head)
			if err != nil {
				return err
			}
			files = map[string]bool{}
			for _, f := range diff.Files {
				files[normPath(f)] = true
			}
			cache[q.Commit] = files
		}
		for _, p := range q.CitedPaths {
			if files[normPath(p)] {
				n.OpenQuestions[i].Status = model.OQStale
				break
			}
		}
	}
	return nil
}

func (d Deps) harvested(ctx context.Context, card model.Card) (string, string, model.Outcome, error) {
	if d.Store == nil {
		return "", "", "", nil
	}
	attempts, err := d.Store.ListAttempts(ctx, card.ID)
	if err != nil {
		return "", "", "", err
	}
	cur := currentAttempt(attempts)
	if cur == nil {
		return "", "", "", nil
	}
	text, err := attemptResult(cur.Worktree)
	if err != nil {
		return "", "", "", err
	}
	return text, cur.BaseCommit, cur.Outcome, nil
}

// currentAttempt is the attempt under disposal: the highest index.
// A missing result file does not fall through to an older attempt.
func currentAttempt(attempts []model.Attempt) *model.Attempt {
	var best *model.Attempt
	for i := range attempts {
		if best == nil || attempts[i].Index > best.Index {
			cp := attempts[i]
			best = &cp
		}
	}
	return best
}

func attemptResult(worktree string) (string, error) {
	wt := strings.TrimSpace(worktree)
	if wt == "" {
		return "", nil
	}
	raw, err := os.ReadFile(filepath.Join(wt, ".cardex-result.md"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(raw), nil
}

func (d Deps) factList(ctx context.Context, obj model.Objective, node model.DesignNode) ([]model.OpenQuestion, error) {
	dir, ok, err := d.rootDirection(ctx, obj.ID)
	if err != nil {
		return nil, err
	}
	var out []model.OpenQuestion
	seen := map[string]bool{}
	add := func(q model.OpenQuestion) {
		if q.Type != model.OQFact || q.ID == "" || seen[q.ID] {
			return
		}
		seen[q.ID] = true
		out = append(out, q)
	}
	if ok {
		if err := d.markStale(ctx, obj, &dir); err != nil {
			return nil, err
		}
		if err := d.Store.UpdateNode(ctx, &dir); err != nil {
			return nil, err
		}
		for _, q := range dir.OpenQuestions {
			if q.ID == "Q0" {
				add(q)
			}
		}
	}
	for _, q := range node.OpenQuestions {
		add(q)
	}
	return out, nil
}

func summaryBody(intro string, facts []model.OpenQuestion) (string, error) {
	marked := make([]model.OpenQuestion, 0, len(facts))
	for _, q := range facts {
		marked = append(marked, markSummary(q))
	}
	if marked == nil {
		marked = []model.OpenQuestion{}
	}
	raw, err := json.Marshal(marked)
	if err != nil {
		return "", err
	}
	return intro + "\n## 事实摘要\n" + string(raw) + "\n", nil
}

func normPath(p string) string {
	p = filepath.ToSlash(strings.TrimSpace(p))
	p = strings.TrimPrefix(p, "./")
	return p
}
