package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// cmdHarvest re-judges finished attempts by worktree outcome.
// The default is dry-run: print a verdict and do not change status.
// -apply stores Verdict/Harvest and appends a harvested event. Status changes
// only when the verdict is done because verify passed.
func cmdHarvest(args []string) error {
	fs := flag.NewFlagSet("harvest", flag.ExitOnError)
	rootFlag := fs.String("root", "", "数据目录")
	apply := fs.Bool("apply", false, "写入 verdict/harvest；仅 verify 通过的 done 会改状态")
	asJSON := fs.Bool("json", false, "输出 JSON 数组")
	held := fs.Bool("held", false, "重判 last_error 属于未知结局的 held 卡")
	_ = fs.Parse(args)
	root := resolveRoot(*rootFlag)
	cfg, cfgErr := loadConfig(root)

	tasks, err := loadTasks(root)
	if err != nil {
		return err
	}
	byID := map[string]*Task{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	var selected []*Task
	if fs.NArg() > 0 {
		for _, id := range fs.Args() {
			id = strings.TrimSpace(id)
			t, ok := byID[id]
			if !ok {
				loaded, loadErr := loadTask(root, id)
				if loadErr != nil {
					return fmt.Errorf("任务 %s 不存在", id)
				}
				t = loaded
			}
			if *held && !harvestHeldCandidate(t) {
				continue
			}
			selected = append(selected, t)
		}
	} else if *held {
		for _, t := range tasks {
			if harvestHeldCandidate(t) {
				selected = append(selected, t)
			}
		}
	} else {
		return fmt.Errorf("用法: cardex harvest [-root R] [-apply] [-json] [-held] [ID ...]")
	}

	shared := map[string]int{}
	for _, t := range tasks {
		if t.Dir == "" {
			continue
		}
		shared[filepath.Clean(t.Dir)]++
	}

	var lines []harvestCLILine
	for _, t := range selected {
		opts := harvestOpts{SkipVerify: !*apply, Historical: strings.TrimSpace(t.BaseCommit) == ""}
		if opts.Historical && t.Dir != "" && shared[filepath.Clean(t.Dir)] > 1 {
			opts.SharedDirNote = "other tasks share this dir; historical commits are not card-exclusive"
		}
		var useCfg *Config
		if cfgErr == nil {
			useCfg = cfg
		}
		ev, verdict, reason := harvestAttemptOpts(root, useCfg, t, "", nil, nil, opts)
		line := harvestCLILine{
			ID:           t.ID,
			Verdict:      verdict,
			Reason:       reason,
			ChangedFiles: len(ev.ChangedFiles),
			Insertions:   ev.Insertions,
			Deletions:    ev.Deletions,
			Commits:      len(ev.NewCommits),
			Report:       ev.ReportExcerpt != "",
			PatchPath:    ev.PatchPath,
		}
		lines = append(lines, line)
		if *apply {
			t.Verdict = verdict
			t.Harvest = ev
			if verdict == harvestVerdictDone && reason == harvestReasonVerifyPassed {
				t.Status = statusDone
				t.LastError = ""
			}
			t.touch()
			if err := saveTask(root, t); err != nil {
				return err
			}
			emitTaskEvent(root, t.ID, evHarvested, "cli:harvest", t.Status, t.Step, map[string]any{
				"verdict": verdict, "reason": reason,
				"changed_files": line.ChangedFiles, "insertions": line.Insertions,
				"deletions": line.Deletions, "new_commits": line.Commits,
				"patch_path": line.PatchPath,
			})
		}
	}
	if lines == nil {
		lines = []harvestCLILine{}
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(lines)
	}
	for _, line := range lines {
		report := "no"
		if line.Report {
			report = "yes"
		}
		fmt.Printf("%s\t%s\t%s\tfiles=%d\t+%d\t-%d\tcommits=%d\treport=%s\tpatch=%s\n",
			line.ID, line.Verdict, line.Reason, line.ChangedFiles, line.Insertions, line.Deletions, line.Commits, report, line.PatchPath)
	}
	return nil
}

type harvestCLILine struct {
	ID           string `json:"id"`
	Verdict      string `json:"verdict"`
	Reason       string `json:"reason"`
	ChangedFiles int    `json:"changed_files"`
	Insertions   int    `json:"insertions"`
	Deletions    int    `json:"deletions"`
	Commits      int    `json:"commits"`
	Report       bool   `json:"report"`
	PatchPath    string `json:"patch_path,omitempty"`
}

func harvestHeldCandidate(t *Task) bool {
	if t == nil || t.Status != statusHeld {
		return false
	}
	errText := strings.ToLower(t.LastError)
	needles := []string{
		"unknown outcome",
		"stream incomplete",
		"invalid terminal",
		"zero-event",
		"native terminal missing",
	}
	for _, n := range needles {
		if strings.Contains(errText, n) {
			return true
		}
	}
	return false
}
