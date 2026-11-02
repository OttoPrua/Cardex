package prompt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"sort"
	"strings"

	"cardex/internal/model"
)

const (
	contextStart = "<!-- cardex-context -->\n"
	contextEnd   = "<!-- /cardex-context -->\n"
	nodesStart   = "<!-- cardex-nodes -->\n"
	nodesEnd     = "<!-- /cardex-nodes -->\n"
	factsStart   = "<!-- cardex-facts -->\n"
	factsEnd     = "<!-- /cardex-facts -->\n"
	cardStart    = "<!-- cardex-card -->\n"

	defaultDesignDir = "docs/design/"
)

// Build concatenates the kind preface, context pack, node docs, fact summary, and the card.
// Context and node documents come from the committed revision, not the worktree.
func Build(ctx context.Context, in Input) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	preface, err := Preface(in.Card.Kind)
	if err != nil {
		return "", err
	}
	// Ranking material arrives on ReworkNote, an input Build already accepted.
	// The source block marks that text as the preface. A normal rework note stays on the card.
	if in.Card.Kind == model.KindRanking && strings.Contains(in.ReworkNote, "<!-- sources:start -->") {
		preface = in.ReworkNote
		in.ReworkNote = ""
	}
	contextBytes, err := loadContext(ctx, in)
	if err != nil {
		return "", err
	}
	nodeBytes, err := loadNodes(ctx, in)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(preface)
	if !strings.HasSuffix(preface, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString(contextStart)
	b.WriteString(contextBytes)
	b.WriteString(contextEnd)
	b.WriteString(nodesStart)
	b.WriteString(nodeBytes)
	b.WriteString(nodesEnd)
	if wantsFacts(in.Card.Kind) {
		b.WriteString(factsStart)
		b.WriteString(renderFacts(in.Facts))
		b.WriteString(factsEnd)
	}
	b.WriteString(cardStart)
	b.WriteString(renderCard(in))
	return b.String(), nil
}

func wantsFacts(kind model.CardKind) bool {
	return kind == model.KindDesign || kind == model.KindPlan || kind == model.KindReplan
}

func loadContext(ctx context.Context, in Input) (string, error) {
	if in.Context != "" {
		return in.Context, nil
	}
	if strings.TrimSpace(in.Repo) == "" || strings.TrimSpace(in.Revision) == "" {
		return "", nil
	}
	obj := in.Objective.ID
	if obj == "" {
		obj = in.Card.Objective
	}
	return readRevision(ctx, in.Repo, in.Revision, contextPath(in.DesignDir, obj))
}

func contextPath(designDir, objective string) string {
	if strings.TrimSpace(designDir) == "" {
		designDir = defaultDesignDir
	}
	return path.Join(path.Clean(designDir), objective, "context.md")
}

func loadNodes(ctx context.Context, in Input) (string, error) {
	nodes := append([]model.DesignNode(nil), in.Nodes...)
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].DocPath == nodes[j].DocPath {
			return nodes[i].ID < nodes[j].ID
		}
		return nodes[i].DocPath < nodes[j].DocPath
	})
	var b strings.Builder
	for _, node := range nodes {
		docPath := strings.TrimSpace(node.DocPath)
		if docPath == "" {
			continue
		}
		body, err := readRevision(ctx, in.Repo, in.Revision, path.Clean(docPath))
		if err != nil {
			return "", err
		}
		b.WriteString(docPath)
		b.WriteByte('\n')
		b.WriteString(body)
	}
	return b.String(), nil
}

// readRevision returns the blob at revision:path. A path missing from that
// revision is empty. Other git failures are returned.
func readRevision(ctx context.Context, repo, revision, filePath string) (string, error) {
	if strings.TrimSpace(repo) == "" || strings.TrimSpace(revision) == "" {
		return "", fmt.Errorf("prompt: repo and revision required to read %s", filePath)
	}
	spec := revision + ":" + filePath
	cmd := exec.CommandContext(ctx, "git", "--no-pager", "-C", repo, "show", spec)
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && blobMissing(exit.Stderr) {
			return "", nil
		}
		return "", fmt.Errorf("prompt: git show %s: %w", spec, err)
	}
	return string(out), nil
}

func blobMissing(stderr []byte) bool {
	return bytes.Contains(stderr, []byte("does not exist")) ||
		bytes.Contains(stderr, []byte("exists on disk, but not in"))
}

func renderFacts(questions []model.OpenQuestion) string {
	var b strings.Builder
	for _, q := range questions {
		fmt.Fprintf(&b, "问题编号：%s\n问题：%s\n结论：%s\n证据：%s\n把握：%s\n",
			q.ID, q.Text, q.Answer, q.Evidence, q.Confidence)
		switch q.Status {
		case model.OQStale:
			b.WriteString("可能过期\n")
		case model.OQUnanswered:
			b.WriteString("未能核实\n")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func renderCard(in Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, "标题：%s\n正文：\n%s\n", in.Card.Title, in.Card.Body)
	b.WriteString("声明：\n")
	for _, claim := range in.Card.Claims {
		fmt.Fprintf(&b, "- %s %s\n", claim.Path, claim.Symbol)
	}
	b.WriteString("验收命令：\n")
	for _, cmd := range in.Card.Acceptance {
		fmt.Fprintf(&b, "- %s\n", cmd)
	}
	note := in.ReworkNote
	if note == "" {
		note = in.Card.ReworkNote
	}
	if strings.TrimSpace(note) != "" {
		fmt.Fprintf(&b, "返工意见：\n%s\n", note)
	}
	return b.String()
}
