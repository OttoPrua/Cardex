package harvest

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/proc"
	"github.com/OttoPrua/Cardex/internal/roots"
)

// FileChange is one path changed between the attempt's base and its worktree.
type FileChange struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	Binary  bool   `json:"binary,omitempty"`
}

// AcceptanceResult is one acceptance command and, for go test, the baseline comparison.
type AcceptanceResult struct {
	Command    string        `json:"command"`
	Exit       int           `json:"exit"`
	DurationMS int64         `json:"duration_ms"`
	Baseline   *TestBaseline `json:"baseline,omitempty"`
}

// ExpectedRun is the channel, model, or review tier the attempt was supposed to use.
type ExpectedRun struct {
	Channel string `json:"channel,omitempty"`
	Model   string `json:"model,omitempty"`
	Tier    string `json:"tier,omitempty"`
}

// Summary is the structured record written when an attempt finishes.
type Summary struct {
	AttemptID  string             `json:"attempt_id"`
	Runner     string             `json:"runner"`
	Channel    string             `json:"channel"`
	Model      string             `json:"model"`
	Tier       model.Tier         `json:"tier,omitempty"`
	Expected   ExpectedRun        `json:"expected"`
	Match      bool               `json:"match"`
	BaseCommit string             `json:"base_commit"`
	HeadCommit string             `json:"head_commit"`
	Files      []FileChange       `json:"files"`
	Acceptance []AcceptanceResult `json:"acceptance"`
	Review     *ReviewSummary     `json:"review,omitempty"`
}

type commandRun struct {
	Command  string
	Exit     int
	Duration time.Duration
	Output   []byte
}

// SummaryPath keeps the summary alongside the attempt's other evidence.
func SummaryPath(home string, attempt model.Attempt) string {
	return filepath.Join(roots.AttemptDir(home, attempt.ID, attempt.PromptPath), "summary.json")
}

// LoadSummary reads a summary file.
func LoadSummary(path string) (Summary, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Summary{}, err
	}
	var sum Summary
	if err := json.Unmarshal(body, &sum); err != nil {
		return Summary{}, err
	}
	return sum, nil
}

// FormatSummary renders a summary as plain text. JSON output uses the struct itself.
func FormatSummary(sum Summary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "attempt %s\n", sum.AttemptID)
	fmt.Fprintf(&b, "runner %s\n", sum.Runner)
	fmt.Fprintf(&b, "channel %s\n", sum.Channel)
	fmt.Fprintf(&b, "model %s\n", sum.Model)
	if sum.Tier != model.TierNone {
		fmt.Fprintf(&b, "tier %s\n", sum.Tier)
	}
	fmt.Fprintf(&b, "expected channel %s model %s tier %s\n", sum.Expected.Channel, sum.Expected.Model, sum.Expected.Tier)
	fmt.Fprintf(&b, "match %t\n", sum.Match)
	fmt.Fprintf(&b, "base %s\n", sum.BaseCommit)
	fmt.Fprintf(&b, "head %s\n", sum.HeadCommit)
	b.WriteString("files\n")
	for _, file := range sum.Files {
		fmt.Fprintf(&b, "  %s +%d -%d\n", file.Path, file.Added, file.Deleted)
	}
	b.WriteString("acceptance\n")
	for _, cmd := range sum.Acceptance {
		fmt.Fprintf(&b, "  %s exit=%d duration_ms=%d\n", cmd.Command, cmd.Exit, cmd.DurationMS)
		if cmd.Baseline == nil {
			continue
		}
		fmt.Fprintf(&b, "  baseline %s", cmd.Baseline.Status)
		if cmd.Baseline.Exit != nil {
			fmt.Fprintf(&b, " exit=%d", *cmd.Baseline.Exit)
		}
		b.WriteByte('\n')
		if cmd.Baseline.Status == "compared" {
			writeFailures(&b, "new", cmd.Baseline.New)
			writeNames(&b, "gone", cmd.Baseline.Gone)
			writeNames(&b, "still", cmd.Baseline.Still)
		}
	}
	if sum.Review != nil {
		fmt.Fprintf(&b, "review %s\n", sum.Review.Verdict)
		writeIssues(&b, "P0", sum.Review.P0)
		writeIssues(&b, "P1", sum.Review.P1)
		writeIssues(&b, "P2", sum.Review.P2)
		b.WriteString("tests\n")
		for _, name := range sum.Review.Tests {
			fmt.Fprintf(&b, "  %s\n", name)
		}
		if sum.Review.Source != "" {
			fmt.Fprintf(&b, "source %s\n", sum.Review.Source)
		}
	}
	return b.String()
}

func writeFailures(b *strings.Builder, label string, items []TestFailure) {
	fmt.Fprintf(b, "  %s\n", label)
	for _, item := range items {
		fmt.Fprintf(b, "    %s\n", item.Name)
		if item.Context == "" {
			continue
		}
		for _, line := range strings.Split(item.Context, "\n") {
			fmt.Fprintf(b, "      %s\n", line)
		}
	}
}

func writeNames(b *strings.Builder, label string, items []string) {
	fmt.Fprintf(b, "  %s\n", label)
	for _, name := range items {
		fmt.Fprintf(b, "    %s\n", name)
	}
}

func writeIssues(b *strings.Builder, label string, items []ReviewIssue) {
	fmt.Fprintf(b, "  %s\n", label)
	for _, item := range items {
		fmt.Fprintf(b, "    %s:%d %s\n", item.File, item.Line, item.Text)
	}
}

func buildSummary(ctx context.Context, in Input, dir, base string, acceptance []AcceptanceResult, raw string, haveResult bool) Summary {
	head := strings.TrimSpace(in.Attempt.HeadCommit)
	if head == "" && dir != "" {
		head, _ = gitOut(ctx, dir, "rev-parse", "HEAD")
	}
	sum := Summary{
		AttemptID:  strings.TrimSpace(in.Attempt.ID),
		Runner:     strings.TrimSpace(in.Runner),
		Channel:    strings.TrimSpace(in.Attempt.Route.Channel),
		Model:      strings.TrimSpace(in.Attempt.Route.Model),
		Tier:       in.Attempt.RouteReason.SelectedTier(in.Attempt.Route),
		Expected:   ExpectedRun{Channel: strings.TrimSpace(in.ExpectedChannel), Model: strings.TrimSpace(in.ExpectedModel), Tier: strings.TrimSpace(in.ExpectedTier)},
		BaseCommit: base,
		HeadCommit: head,
		Files:      diffFiles(ctx, dir, base),
		Acceptance: acceptance,
	}
	if sum.Files == nil {
		sum.Files = []FileChange{}
	}
	if sum.Acceptance == nil {
		sum.Acceptance = []AcceptanceResult{}
	}
	sum.Match = routeMatches(in, sum.Channel, sum.Model)
	if in.Card.Kind == model.KindReview {
		review := parseReviewSummary(raw)
		if haveResult && safeAttemptID(sum.AttemptID) {
			review.Source = roots.EvidenceRef(in.Home, sum.AttemptID, in.Attempt.PromptPath, "result.md")
		} else if haveResult {
			review.Source = ".cardex-result.md"
		}
		sum.Review = &review
	}
	return sum
}

// WriteUnavailableSummary records an ended attempt whose checkout could not be harvested.
func WriteUnavailableSummary(ctx context.Context, in Input) error {
	sum := buildSummary(ctx, in, "", in.Attempt.BaseCommit, nil, "", false)
	return writeSummary(in.Home, in.Attempt, sum)
}

func routeMatches(in Input, channel, modelName string) bool {
	if want := strings.TrimSpace(in.ExpectedChannel); want != "" && channel != want {
		return false
	}
	if want := strings.TrimSpace(in.ExpectedModel); want != "" && modelName != want {
		return false
	}
	want := model.Tier(strings.TrimSpace(in.ExpectedTier))
	if want.Rank() == 0 {
		return true
	}
	return in.Attempt.RouteReason.SelectedTier(model.Route{Channel: channel, Model: modelName}).Rank() >= want.Rank()
}

func acceptanceResults(ctx context.Context, home, dir, base string, runs []commandRun, env []string, scratch string) []AcceptanceResult {
	out := make([]AcceptanceResult, 0, len(runs))
	for _, run := range runs {
		item := AcceptanceResult{
			Command:    run.Command,
			Exit:       run.Exit,
			DurationMS: run.Duration.Milliseconds(),
		}
		if isGoTest(run.Command) {
			item.Baseline = compareBaseline(ctx, home, dir, base, run, env, scratch)
		}
		out = append(out, item)
	}
	return out
}

func diffFiles(ctx context.Context, dir, base string) []FileChange {
	if strings.TrimSpace(base) == "" || strings.TrimSpace(dir) == "" {
		return []FileChange{}
	}
	text, err := gitExec(ctx, dir, "diff", "--numstat", "-z", "--no-renames", base, "--")
	if err != nil {
		return []FileChange{}
	}
	var files []FileChange
	seen := map[string]bool{}
	for _, line := range strings.Split(string(text), "\x00") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		path := parts[2]
		if path == "" || path == ".cardex-result.md" || seen[path] {
			continue
		}
		seen[path] = true
		item := FileChange{Path: path}
		if parts[0] == "-" || parts[1] == "-" {
			item.Binary = true
		} else {
			fmt.Sscan(parts[0], &item.Added)
			fmt.Sscan(parts[1], &item.Deleted)
		}
		files = append(files, item)
	}
	others, err := gitExec(ctx, dir, "ls-files", "-z", "--others", "--exclude-standard")
	if err == nil {
		for _, path := range strings.Split(string(others), "\x00") {
			if path == "" || path == ".cardex-result.md" || seen[path] {
				continue
			}
			seen[path] = true
			files = append(files, untrackedChange(dir, path))
		}
	}
	if files == nil {
		return []FileChange{}
	}
	return files
}

func untrackedChange(dir, path string) FileChange {
	item := FileChange{Path: path}
	path = filepath.Join(dir, filepath.FromSlash(path))
	info, err := os.Lstat(path)
	if err != nil {
		return item
	}
	if info.Mode()&os.ModeSymlink != 0 {
		item.Added = 1
		return item
	}
	if !info.Mode().IsRegular() {
		return item
	}
	file, err := os.Open(path)
	if err != nil {
		return item
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 8192)
	probe, _ := reader.Peek(8192)
	if strings.IndexByte(string(probe), 0) >= 0 {
		item.Binary = true
		return item
	}
	pending := false
	for {
		line, err := reader.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			pending = true
			continue
		}
		if len(line) > 0 || pending {
			item.Added++
		}
		if err != nil {
			break
		}
		pending = false
	}
	return item
}

func writeSummary(home string, attempt model.Attempt, sum Summary) error {
	attempt.ID = strings.TrimSpace(attempt.ID)
	if strings.TrimSpace(home) == "" || !safeAttemptID(attempt.ID) {
		return nil
	}
	path := SummaryPath(home, attempt)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(sum, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return os.WriteFile(path, body, 0o644)
}

func safeAttemptID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\\`)
}

// gitExec is gitOut's body, split so tests can see the command shape stays portable.
func gitExec(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(proc.AcceptanceEnv(os.Environ()), "GIT_TERMINAL_PROMPT=0")
	return cmd.Output()
}
