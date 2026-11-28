package loop

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

// Review policy, docs/review-policy.md.
//
// A passed dev-family card resolves its review mode card → objective →
// project; the first non-empty value wins and all empty means auto.
// auto is review for dev-family kinds. off merges without a review card.
// check opens a T3 completion check. review opens a review whose tier
// follows the diff: small and low-risk goes to T3, risky or large to T1,
// the rest stays at the author's tier.

// Review verdicts beyond pass and rework.
const (
	verdictBlocked  = "blocked"  // 复核方无法核实（环境、权限、缺资料）：转人工，不计返工。
	verdictEscalate = "escalate" // 复核方没把握：升一档重新复核，不计返工。
)

// Diff thresholds for the review tier.
const (
	smallDiffLines = 60
	smallDiffFiles = 3
	largeDiffLines = 600
	largeDiffFiles = 20
)

var riskyPath = regexp.MustCompile(`(?i)(sandbox|permission|auth|secret|credential|token|password|migrat|schema|security|crypto|\.github/workflows/|dockerfile|\.plist$|launchd)`)

type reviewPlan struct {
	Mode model.ReviewMode
	Tier model.Tier
	Why  string
}

// reviewModeFor resolves card, then objective, then project.
func reviewModeFor(card model.Card, obj model.Objective, project model.Project) model.ReviewMode {
	for _, v := range []string{card.Review, obj.Review, project.Review} {
		if m := model.ReviewMode(strings.TrimSpace(v)); m != model.ReviewInherit {
			if m == model.ReviewAuto {
				break
			}
			return m
		}
	}
	return model.ReviewFull
}

type diffStat struct {
	Files int
	Lines int
	Paths []string
}

// planReview decides the mode and, for check and review, the reviewer tier.
func planReview(card model.Card, obj model.Objective, project model.Project, stat *diffStat) reviewPlan {
	mode := reviewModeFor(card, obj, project)
	switch mode {
	case model.ReviewOff:
		return reviewPlan{Mode: mode, Why: "review off"}
	case model.ReviewCheck:
		return reviewPlan{Mode: mode, Tier: model.TierT3, Why: "completion check"}
	}
	author := card.Tier
	if author == "" {
		author = model.DefaultTier(card.Kind)
	}
	if stat == nil {
		return reviewPlan{Mode: model.ReviewFull, Tier: model.TierT1, Why: "diff unavailable"}
	}
	for _, p := range stat.Paths {
		if riskyPath.MatchString(p) || forbiddenHit(p, project.ForbiddenPaths) {
			return reviewPlan{Mode: model.ReviewFull, Tier: model.TierT1, Why: "risky path " + p}
		}
	}
	if stat.Lines > largeDiffLines || stat.Files > largeDiffFiles {
		return reviewPlan{Mode: model.ReviewFull, Tier: model.TierT1, Why: fmt.Sprintf("large diff %d lines %d files", stat.Lines, stat.Files)}
	}
	if stat.Lines <= smallDiffLines && stat.Files <= smallDiffFiles {
		return reviewPlan{Mode: model.ReviewFull, Tier: model.TierT3, Why: fmt.Sprintf("small diff %d lines %d files", stat.Lines, stat.Files)}
	}
	return reviewPlan{Mode: model.ReviewFull, Tier: author, Why: fmt.Sprintf("author tier, diff %d lines %d files", stat.Lines, stat.Files)}
}

func forbiddenHit(path string, forbidden []string) bool {
	for _, f := range forbidden {
		f = strings.TrimSpace(f)
		if f != "" && (path == f || strings.HasPrefix(path, strings.TrimSuffix(f, "/")+"/")) {
			return true
		}
	}
	return false
}

// attemptDiff reads git numstat between the attempt's base and head.
// nil means the diff could not be read.
func attemptDiff(ctx context.Context, project model.Project, attempt model.Attempt) *diffStat {
	base, head := strings.TrimSpace(attempt.BaseCommit), strings.TrimSpace(attempt.HeadCommit)
	if base == "" || head == "" {
		return nil
	}
	for _, dir := range []string{project.RepoPath, attempt.Worktree} {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		out, err := exec.CommandContext(cctx, "git", "-C", dir, "diff", "--numstat", "--no-renames", base, head).Output()
		cancel()
		if err != nil {
			continue
		}
		return parseNumstat(string(out))
	}
	return nil
}

func parseNumstat(out string) *diffStat {
	stat := &diffStat{}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		stat.Files++
		stat.Paths = append(stat.Paths, parts[2])
		add, errA := strconv.Atoi(parts[0])
		del, errD := strconv.Atoi(parts[1])
		if errA != nil || errD != nil {
			// Binary file: count it as a large change of one file.
			stat.Lines += 100
			continue
		}
		stat.Lines += add + del
	}
	return stat
}

func reviewBody(plan reviewPlan, family string) string {
	var b strings.Builder
	switch plan.Mode {
	case model.ReviewCheck:
		b.WriteString("复核模式：check（完成度检查）。只对照卡的目标和交付物清单，检查是否都做到了；不评审代码质量。\n")
	default:
		fmt.Fprintf(&b, "复核模式：review，档位 %s（%s）。\n", plan.Tier, plan.Why)
	}
	if family != "" {
		fmt.Fprintf(&b, "作者模型族：%s。优先换族复核；没有别族可用时允许同族，最后才是同模型新会话。\n", family)
	}
	return b.String()
}

// ReviewHoldMarker starts the floor a rework review must keep.
const ReviewHoldMarker = model.ReviewHoldMarker

// AppendReviewHold records the tier and model pin a later launch must not lower.
func AppendReviewHold(body string, tier model.Tier, channel, modelName string) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteByte('\n')
	b.WriteString(ReviewHoldMarker)
	b.WriteByte('\n')
	wire, _ := model.TranslateLegacyTier(tier)
	fmt.Fprintf(&b, "tier=%s\n", wire)
	if channel != "" {
		fmt.Fprintf(&b, "channel=%s\n", channel)
	}
	if modelName != "" {
		fmt.Fprintf(&b, "model=%s\n", modelName)
	}
	return b.String()
}

// ReviewHold reads the floor written by AppendReviewHold.
// ok is false when the card has no floor.
func ReviewHold(card model.Card) (tier model.Tier, channel, modelName string, ok bool) {
	if card.Kind != "" && card.Kind != model.KindReview {
		return "", "", "", false
	}
	tier, channel, modelName, found, err := model.ParseReviewHold(card.Body)
	return tier, channel, modelName, found && err == nil
}

func raiseReviewFloor(plan reviewPlan, prior model.Card) reviewPlan {
	if plan.Mode == model.ReviewOff {
		plan.Mode = model.ReviewFull
		if prior.Review == string(model.ReviewCheck) {
			plan.Mode = model.ReviewCheck
		}
		plan.Why = "rework keeps review"
	}
	if prior.Review == string(model.ReviewFull) && plan.Mode == model.ReviewCheck {
		plan.Mode = model.ReviewFull
		if plan.Why == "completion check" {
			plan.Why = "rework keeps review"
		}
	}
	if prior.Tier.Rank() > plan.Tier.Rank() {
		plan.Tier = prior.Tier
		plan.Why = "rework keeps " + string(prior.Tier) + "; " + plan.Why
	}
	return plan
}

func reviewHoldUnmetReason(tier model.Tier, channel, modelName string) string {
	return fmt.Sprintf("review hold unmet: tier %s channel %s model %s is not available without lowering the tier", tier, channel, modelName)
}

// ReviewFloorMet reports whether channel and model can take a review at floor.
// An empty channel and an empty model means there is no pin to miss.
func ReviewFloorMet(channels []model.Channel, rows []model.Positioning, channel, modelName string, floor model.Tier) bool {
	return reviewConstraintMet(channels, rows, channel, modelName, floor)
}

func reviewConstraintMet(channels []model.Channel, rows []model.Positioning, channel, modelName string, floor model.Tier) bool {
	if strings.TrimSpace(channel) == "" && strings.TrimSpace(modelName) == "" {
		return true
	}
	if floor.Rank() == 0 {
		return false
	}
	for _, ch := range channels {
		if channel != "" && ch.ID != channel {
			continue
		}
		if ch.Health != "" && ch.Health != model.HealthOK {
			if channel != "" {
				return false
			}
			continue
		}
		names := ch.Models
		if modelName != "" {
			if !channelOffersModel(ch, modelName) {
				continue
			}
			names = []string{modelName}
		}
		for _, name := range names {
			if modelMeetsFloor(rows, ch, name, floor) {
				return true
			}
		}
	}
	return false
}

func channelOffersModel(ch model.Channel, name string) bool {
	for _, item := range ch.Models {
		if item == name {
			return true
		}
	}
	return false
}

func modelMeetsFloor(rows []model.Positioning, ch model.Channel, name string, floor model.Tier) bool {
	saw := false
	for _, row := range rows {
		if row.Model == "" || row.Model != name {
			continue
		}
		saw = true
		if len(row.Kinds) != 0 && !slices.Contains(row.Kinds, model.KindReview) {
			continue
		}
		for _, tier := range row.Tiers {
			if tier.Rank() >= floor.Rank() {
				return true
			}
		}
	}
	if saw {
		return false
	}
	return ch.Tier.Rank() >= floor.Rank()
}

func upTier(t model.Tier) (model.Tier, bool) {
	switch t {
	case model.TierT3:
		return model.TierT2, true
	case model.TierT2:
		return model.TierT1, true
	default:
		return t, false
	}
}
