package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"
)

// errRunOnceUsage is the CLI-shape failure for `cardex run -once` without exactly
// one nonempty ID. Parse returns this before loadConfig / tick so tests can drive
// it without a fake provider or a real root.
var errRunOnceUsage = errors.New("run -once requires exactly one nonempty ID")

// errRunOnceIncomplete is a dispatched -once attempt whose real disposition is
// not done. Process exit 0 would pretend the attempt succeeded.
var errRunOnceIncomplete = errors.New("run -once attempt did not complete successfully")

// errRunOnceIsolation is fail-closed when -once cannot keep non-target cards
// unmodified (follow-on spawn / fanout / cross-chain / review children).
var errRunOnceIsolation = errors.New("run -once isolation cannot be guaranteed")

type runCmdOpts struct {
	root  string
	force bool
	quiet bool
	once  bool
	id    string
}

func parseRunArgs(args []string) (runCmdOpts, error) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	setCmdRunUsage(fs)
	rootFlag := fs.String("root", "", "数据目录")
	force := fs.Bool("force", false, "忽略限额冷却强行尝试")
	quiet := fs.Bool("quiet", false, "静默模式（launchd 用）")
	once := fs.Bool("once", false, "对该 ID 最多一次 Cardex 派发尝试，等待结束后返回")
	if err := fs.Parse(args); err != nil {
		return runCmdOpts{}, err
	}
	if fs.NArg() > 1 {
		if *once {
			return runCmdOpts{}, fmt.Errorf("%w: missing/empty/multiple IDs and all-queue use are rejected", errRunOnceUsage)
		}
		return runCmdOpts{}, fmt.Errorf("用法: cardex run [-root ROOT] [-force] [-quiet] [-once] [ID]")
	}
	id := ""
	if fs.NArg() == 1 {
		id = strings.TrimSpace(fs.Arg(0))
	}
	if *once {
		if fs.NArg() != 1 || id == "" {
			return runCmdOpts{}, fmt.Errorf("%w: missing/empty/multiple IDs and all-queue use are rejected", errRunOnceUsage)
		}
	}
	return runCmdOpts{root: *rootFlag, force: *force, quiet: *quiet, once: *once, id: id}, nil
}

// onceFollowOnWouldEscape reports shapes whose post-run path would create or
// mutate a non-target card. -once refuses them rather than spawning children.
func onceFollowOnWouldEscape(t *Task) error {
	if t == nil {
		return nil
	}
	if t.ReviewAfter && reviewAfterEligibleType(t) {
		return fmt.Errorf("%w: %s has review_after and would spawn a review child", errRunOnceIsolation, t.ID)
	}
	if t.EmitTasks {
		return fmt.Errorf("%w: %s emits follow-on tasks", errRunOnceIsolation, t.ID)
	}
	if strings.TrimSpace(t.XRole) != "" || strings.TrimSpace(t.XKey) != "" {
		return fmt.Errorf("%w: %s is a cross-chain card", errRunOnceIsolation, t.ID)
	}
	if t.Type == typeReview && t.ReviewPlanStage != "" {
		return fmt.Errorf("%w: %s planned review may spawn the next gate", errRunOnceIsolation, t.ID)
	}
	if t.Type == typeReview && !t.AdvisoryReview {
		return fmt.Errorf("%w: %s review may spawn a repair child", errRunOnceIsolation, t.ID)
	}
	if t.FableFirstPrinciplesReview {
		return fmt.Errorf("%w: %s would spawn a first-principles review child", errRunOnceIsolation, t.ID)
	}
	return nil
}

// onceAttemptDisposition returns nil only when the named card is done after the
// single attempt. Requeue / 429 / failed / held / limit_paused stay nonzero.
func onceAttemptDisposition(root, id string) error {
	t, err := findTask(root, id)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", errRunOnceIncomplete, id, err)
	}
	if t.Status == statusDone {
		return nil
	}
	msg := strings.TrimSpace(t.LastError)
	if msg == "" {
		msg = "no last_error"
	}
	return fmt.Errorf("%w: %s remains %s (attempts=%d last_error=%s)", errRunOnceIncomplete, id, t.Status, t.Attempts, msg)
}
