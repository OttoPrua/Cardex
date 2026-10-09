package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

type digestEntry struct {
	Seq       int64  `json:"seq"`
	TaskID    string `json:"task_id"`
	EventType string `json:"event_type"`
	Verdict   string `json:"verdict"`
	Title     string `json:"title"`
	Evidence  string `json:"evidence"`
	Req       string `json:"req,omitempty"`
}

func cmdDigest(args []string) error {
	fs := flag.NewFlagSet("digest", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	rootFlag := fs.String("root", "", "数据目录")
	sinceFlag := fs.Int64("since", -1, "只显示 seq 大于等于该值的行")
	lastFlag := fs.Int("last", -1, "只显示最后 N 行")
	projectFlag := fs.String("project", "", "项目过滤，逗号分隔")
	managerFlag := fs.String("manager", "", "任务 manager 过滤")
	eventsFlag := fs.String("events", "", "事件类型过滤，逗号分隔")
	jsonFlag := fs.Bool("json", false, "以 JSON 数组输出")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("digest: unexpected arguments %q", strings.Join(fs.Args(), " "))
	}
	root := resolveRoot(*rootFlag)
	rows := readDigestOutbox(root)
	projects := splitCSV(*projectFlag)
	events := splitCSV(*eventsFlag)
	manager := strings.TrimSpace(*managerFlag)

	var matched []digestEntry
	for _, row := range rows {
		if *sinceFlag >= 0 && row.Seq < *sinceFlag {
			continue
		}
		if len(events) > 0 && !containsFold(events, row.EventType) {
			continue
		}
		task, _ := findTaskAnywhere(root, row.TaskID)
		project := row.Project
		if task != nil && strings.TrimSpace(task.Project) != "" {
			project = task.Project
		}
		if len(projects) > 0 && !containsFold(projects, project) && !containsFold(projects, row.Project) {
			continue
		}
		if manager != "" {
			if task == nil || !strings.EqualFold(strings.TrimSpace(task.Manager), manager) {
				continue
			}
		}
		matched = append(matched, digestEntryFrom(row, task))
	}
	if *sinceFlag < 0 && *lastFlag < 0 {
		matched = tailDigest(matched, 20)
	} else if *lastFlag >= 0 {
		matched = tailDigest(matched, *lastFlag)
	}
	if *jsonFlag {
		if matched == nil {
			matched = []digestEntry{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(matched)
	}
	for _, entry := range matched {
		fmt.Println(formatDigestLine(entry))
	}
	return nil
}

func readDigestOutbox(root string) []managerWakeOutboxRow {
	f, err := os.Open(managerWakeOutboxPath(root))
	if err != nil {
		return nil
	}
	defer f.Close()
	var rows []managerWakeOutboxRow
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row managerWakeOutboxRow
		if json.Unmarshal([]byte(line), &row) != nil {
			continue
		}
		if row.Schema != outboxSchemaV1 || row.Seq <= 0 || row.TaskID == "" || row.EventType == "" {
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

func digestEntryFrom(row managerWakeOutboxRow, task *Task) digestEntry {
	entry := digestEntry{
		Seq:       row.Seq,
		TaskID:    row.TaskID,
		EventType: row.EventType,
		Verdict:   "-",
		Title:     "",
		Evidence:  "-",
	}
	if task == nil {
		return entry
	}
	entry.Title = clipRunes(task.Title, 50)
	entry.Req = strings.TrimSpace(task.Req)
	if strings.TrimSpace(task.Verdict) != "" {
		entry.Verdict = task.Verdict
	}
	if task.Harvest != nil {
		entry.Evidence = harvestDigestEvidence(task.Harvest)
		return entry
	}
	text := strings.TrimSpace(task.LastError)
	if text == "" {
		text = strings.TrimSpace(task.LastSummary)
	}
	if text != "" {
		entry.Evidence = clipRunes(strings.Join(strings.Fields(text), " "), 80)
	}
	return entry
}

func harvestDigestEvidence(h *HarvestEvidence) string {
	exit := "-"
	if h.VerifyExit != nil {
		exit = fmt.Sprintf("%d", *h.VerifyExit)
	}
	return fmt.Sprintf("files=%d +%d/-%d commits=%d verify=%s", len(h.ChangedFiles), h.Insertions, h.Deletions, len(h.NewCommits), exit)
}

func formatDigestLine(entry digestEntry) string {
	return fmt.Sprintf("#%d %s %s verdict=%s 「%s」 %s", entry.Seq, entry.TaskID, entry.EventType, entry.Verdict, entry.Title, entry.Evidence)
}

func tailDigest(rows []digestEntry, n int) []digestEntry {
	if n <= 0 {
		return nil
	}
	if len(rows) <= n {
		return rows
	}
	return rows[len(rows)-n:]
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func containsFold(list []string, want string) bool {
	for _, item := range list {
		if strings.EqualFold(item, want) {
			return true
		}
	}
	return false
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
