package loop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"cardex/internal/model"
)

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

func (d Deps) head(ctx context.Context, obj model.Objective) string {
	repo, err := d.repoPath(ctx, obj)
	if err != nil || repo == "" {
		return ""
	}
	if obj.IntegrationBranch != "" {
		if h, err := readGitHEAD(repo, obj.IntegrationBranch); err == nil && h != "" {
			return h
		}
	}
	h, _ := readGitHEAD(repo, "")
	return h
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

func (d Deps) absorbResearch(ctx context.Context, obj model.Objective, card model.Card) error {
	node, err := d.Store.GetNode(ctx, card.Node)
	if err != nil {
		return err
	}
	ids, err := d.researchIDs(ctx, card)
	if err != nil {
		return err
	}
	blocks := parseFactSection(splitSections(card.Body)["事实"])
	for _, id := range ids {
		b, ok := blocks[id]
		if !ok {
			b = factBlock{}
		}
		for i := range node.OpenQuestions {
			if node.OpenQuestions[i].ID != id {
				continue
			}
			applyBlock(&node.OpenQuestions[i], b, card.Base)
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
	ids := questionIDsInBody(card.Body)
	for id := range parseFactSection(splitSections(card.Body)["事实"]) {
		ids = append(ids, id)
	}
	return ids, nil
}

func (d Deps) markStale(ctx context.Context, obj model.Objective, n *model.DesignNode) error {
	repo, err := d.repoPath(ctx, obj)
	if err != nil || repo == "" || d.WS == nil {
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
			diff, err := d.WS.Diff(ctx, repo, q.Commit)
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

func readGitHEAD(repo, branch string) (string, error) {
	gitPath := filepath.Join(repo, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return "", err
	}
	gitDir := gitPath
	if !info.IsDir() {
		raw, err := os.ReadFile(gitPath)
		if err != nil {
			return "", err
		}
		line := strings.TrimSpace(string(raw))
		const prefix = "gitdir:"
		if !strings.HasPrefix(line, prefix) {
			return "", os.ErrNotExist
		}
		gitDir = strings.TrimSpace(strings.TrimPrefix(line, prefix))
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(repo, gitDir)
		}
	}
	ref := ""
	if branch == "" {
		raw, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
		if err != nil {
			return "", err
		}
		line := strings.TrimSpace(string(raw))
		if strings.HasPrefix(line, "ref:") {
			ref = strings.TrimSpace(strings.TrimPrefix(line, "ref:"))
		} else if isHex(line) {
			return line, nil
		} else {
			return "", os.ErrNotExist
		}
	} else {
		ref = "refs/heads/" + branch
	}
	if hash, err := os.ReadFile(filepath.Join(gitDir, filepath.FromSlash(ref))); err == nil {
		s := strings.TrimSpace(string(hash))
		if isHex(s) {
			return s, nil
		}
	}
	packed, err := os.ReadFile(filepath.Join(gitDir, "packed-refs"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(packed), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == ref && isHex(parts[0]) {
			return parts[0], nil
		}
	}
	return "", os.ErrNotExist
}

func isHex(s string) bool {
	if len(s) < 7 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
