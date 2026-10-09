package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ProgressEntry 是一次进度回收的落盘格式：固定信封 + 会话给出的报告原文。
// 报告字段约定（progress-brief/progress-dump 模板）：goal / done / in_progress /
// remaining / blockers / key_files / next_prompt，但这里不做强校验，原样保存。
type ProgressEntry struct {
	Key       string         `json:"key"`
	Title     string         `json:"title,omitempty"`
	Dir       string         `json:"dir,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	UpdatedAt string         `json:"updated_at"`
	Report    map[string]any `json:"report"`
}

func progressPath(root, key string) string {
	return filepath.Join(progressDir(root), key+".json")
}

func saveProgress(root string, e *ProgressEntry) error {
	if e.Key == "" {
		return fmt.Errorf("进度报告缺少 key")
	}
	if e.UpdatedAt == "" {
		e.UpdatedAt = time.Now().Format(time.RFC3339)
	}
	if err := os.MkdirAll(progressDir(root), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(progressPath(root, e.Key), append(data, '\n'))
}

// parseReportLoose 尽力把输出解析为进度报告：优先 json 块，其次整体 JSON；
// 都不行则以原文兜底（被 resume 的老会话常按既有风格输出散文，丢弃太可惜——
// 读报告的是协调任务，散文一样能读）。
func parseReportLoose(result string) map[string]any {
	raw := lastFencedJSON(result)
	if raw == "" {
		raw = strings.TrimSpace(result)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(raw), &report); err == nil {
		return report
	}
	text := strings.TrimSpace(result)
	if r := []rune(text); len(r) > 6000 {
		text = string(r[:6000]) + "…（截断）"
	}
	return map[string]any{"format": "raw", "raw": text}
}

// saveProgressFromResult 从任务最终输出中提取进度报告并落盘（EmitProgress 任务用）。
func saveProgressFromResult(root string, t *Task, result string) (string, error) {
	if strings.TrimSpace(result) == "" {
		return "", fmt.Errorf("输出为空，无法生成进度报告")
	}
	report := parseReportLoose(result)
	key := t.ProgressKey
	if key == "" {
		key = t.ID
	}
	e := &ProgressEntry{Key: key, Title: t.Title, Dir: t.Dir, SessionID: t.SessionID, Report: report}
	if err := saveProgress(root, e); err != nil {
		return "", err
	}
	return key, nil
}

func loadProgressEntries(root string) []*ProgressEntry {
	entries, err := os.ReadDir(progressDir(root))
	if err != nil {
		return nil
	}
	var out []*ProgressEntry
	for _, f := range entries {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(progressDir(root), f.Name()))
		if err != nil {
			continue
		}
		var e ProgressEntry
		if json.Unmarshal(data, &e) != nil || e.Key == "" {
			continue
		}
		out = append(out, &e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt < out[j].UpdatedAt })
	return out
}

// ---- 协调任务的运行时上下文注入 ----

// liveContextSnapshotBudget is the modest default byte budget for one
// {{QUEUE}} or {{PROGRESS}} substitution. It was taken from the scoped
// fixture payloads in progress_test.go (in-scope rows plus a required
// cross-project predecessor and omission disclosure).
const liveContextSnapshotBudget = 8192

const (
	liveContextProseRunes         = 800
	liveContextOmissionPointerCap = 8
)

// injectLiveContext 在派发时把 {{QUEUE}} / {{PROGRESS}} 替换为实时快照。
// 协调任务入队和真正运行之间可能隔很久，所以这两块必须运行时取。
// Signature stays (root, selfID, prompt) so unowned dispatch/cmd call sites
// remain compatible. Scope comes from the coordinating task's explicit
// project, workflow, and dependency facts.
func injectLiveContext(root string, selfID, prompt string) string {
	var self *Task
	if strings.TrimSpace(selfID) != "" {
		if t, err := loadTask(root, selfID); err == nil {
			self = t
		}
	}
	if strings.Contains(prompt, "{{QUEUE}}") {
		prompt = strings.ReplaceAll(prompt, "{{QUEUE}}", queueSnapshot(root, self))
	}
	if strings.Contains(prompt, "{{PROGRESS}}") {
		prompt = strings.ReplaceAll(prompt, "{{PROGRESS}}", progressSnapshot(root, self))
	}
	return prompt
}

type liveContextScope struct {
	self        *Task
	project     string
	workflowID  string
	required    []string
	requiredSet map[string]bool
	unscoped    bool
}

type liveContextScopeView struct {
	TaskID     string   `json:"task_id,omitempty"`
	Project    string   `json:"project,omitempty"`
	WorkflowID string   `json:"workflow_id,omitempty"`
	Required   []string `json:"required,omitempty"`
	Unscoped   bool     `json:"unscoped"`
}

type liveContextOmission struct {
	Count    int      `json:"count"`
	Reason   string   `json:"reason"`
	Pointers []string `json:"pointers"`
}

type liveContextItem struct {
	ID         string         `json:"id,omitempty"`
	Key        string         `json:"key,omitempty"`
	Title      string         `json:"title,omitempty"`
	Type       string         `json:"type,omitempty"`
	Status     string         `json:"status,omitempty"`
	Priority   int            `json:"priority,omitempty"`
	Project    string         `json:"project,omitempty"`
	WorkflowID string         `json:"workflow_id,omitempty"`
	DependsOn  []string       `json:"depends_on,omitempty"`
	Dir        string         `json:"dir,omitempty"`
	Log        string         `json:"log,omitempty"`
	Source     string         `json:"source,omitempty"`
	Report     map[string]any `json:"report,omitempty"`
	Required   bool           `json:"required,omitempty"`
	rank       int            `json:"-"`
	pointer    string         `json:"-"`
}

func liveContextScopeFromTask(t *Task) liveContextScope {
	s := liveContextScope{self: t, requiredSet: map[string]bool{}}
	if t == nil {
		s.unscoped = true
		return s
	}
	s.project = strings.TrimSpace(t.Project)
	s.workflowID = strings.TrimSpace(t.WorkflowID)
	for _, id := range t.DependsOn {
		id = strings.TrimSpace(id)
		if id == "" || s.requiredSet[id] {
			continue
		}
		s.requiredSet[id] = true
		s.required = append(s.required, id)
	}
	s.unscoped = s.project == "" && s.workflowID == "" && len(s.required) == 0
	return s
}

func (s liveContextScope) view() liveContextScopeView {
	id := ""
	if s.self != nil {
		id = s.self.ID
	}
	return liveContextScopeView{
		TaskID:     id,
		Project:    s.project,
		WorkflowID: s.workflowID,
		Required:   append([]string(nil), s.required...),
		Unscoped:   s.unscoped,
	}
}

func (s liveContextScope) classifyTask(t *Task, includeTerminal bool) (include bool, required bool, rank int) {
	if t == nil {
		return false, false, 0
	}
	if s.self != nil && t.ID == s.self.ID {
		return false, false, 0
	}
	if s.requiredSet[t.ID] {
		return true, true, 0
	}
	// Queue snapshots hide completed work, but its progress reports remain
	// useful evidence for the next coordinating task.
	if !includeTerminal && t.terminal() {
		return false, false, 0
	}
	if s.unscoped {
		return true, false, 3
	}
	if s.workflowID != "" && t.WorkflowID == s.workflowID {
		return true, false, 1
	}
	if s.project != "" && t.Project == s.project {
		return true, false, 2
	}
	return false, false, 0
}

func queueSnapshot(root string, self *Task) string {
	scope := liveContextScopeFromTask(self)
	tasks, err := loadTasks(root)
	if err != nil {
		return "（读取队列失败）"
	}
	byID := map[string]*Task{}
	for _, t := range tasks {
		if t != nil {
			byID[t.ID] = t
		}
	}
	for _, id := range scope.required {
		if byID[id] != nil {
			continue
		}
		found, findErr := findTaskAnywhere(root, id)
		if findErr == nil && found != nil {
			byID[id] = found
			tasks = append(tasks, found)
		}
	}

	var included []liveContextItem
	var outScope []string
	seen := map[string]bool{}
	for _, t := range tasks {
		if t == nil || seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		inc, req, rank := scope.classifyTask(t, false)
		if !inc {
			if scope.self == nil || t.ID != scope.self.ID {
				outScope = append(outScope, t.ID)
			}
			continue
		}
		item := liveContextItem{
			ID:         t.ID,
			Title:      t.Title,
			Type:       t.Type,
			Status:     t.Status,
			Priority:   t.Priority,
			Project:    t.Project,
			WorkflowID: t.WorkflowID,
			DependsOn:  append([]string(nil), t.DependsOn...),
			Dir:        t.Dir,
			Log:        taskLogPath(root, t.ID),
			Required:   req,
			rank:       rank,
			pointer:    t.ID,
		}
		included = append(included, item)
	}
	for _, id := range scope.required {
		if seen[id] {
			continue
		}
		seen[id] = true
		included = append(included, liveContextItem{
			ID:       id,
			Status:   depWaitMissing,
			Required: true,
			Log:      taskLogPath(root, id),
			rank:     0,
			pointer:  id,
		})
	}
	sortLiveContextItems(included)
	omitted := omissionList("out_of_scope", outScope)
	return renderLiveContextSnapshot(scope, included, omitted, liveContextSnapshotBudget)
}

func progressSnapshot(root string, self *Task) string {
	scope := liveContextScopeFromTask(self)
	entries := loadProgressEntries(root)
	if len(entries) == 0 {
		return "（暂无进度报告。可先用 cardex brief 回收各会话进度再运行协调。）"
	}
	tasks, _ := loadTasks(root)
	byKey := map[string]*Task{}
	for _, t := range tasks {
		if t == nil {
			continue
		}
		byKey[t.ID] = t
		if k := strings.TrimSpace(t.ProgressKey); k != "" {
			byKey[k] = t
		}
	}
	var included []liveContextItem
	var outScope []string
	for _, e := range entries {
		owner := byKey[e.Key]
		inc, req, rank := false, false, 3
		if owner != nil {
			inc, req, rank = scope.classifyTask(owner, true)
		} else if scope.unscoped {
			inc, rank = true, 3
		}
		pointer := "progress:" + e.Key
		if !inc {
			outScope = append(outScope, pointer)
			continue
		}
		src := progressPath(root, e.Key)
		included = append(included, liveContextItem{
			Key:        e.Key,
			Title:      e.Title,
			Dir:        e.Dir,
			Source:     src,
			Report:     boundProgressReport(e.Report),
			Required:   req,
			rank:       rank,
			pointer:    pointer,
			Project:    projectOf(owner),
			WorkflowID: workflowOf(owner),
			ID:         idOf(owner),
		})
	}
	sortLiveContextItems(included)
	omitted := omissionList("out_of_scope", outScope)
	return renderLiveContextSnapshot(scope, included, omitted, liveContextSnapshotBudget)
}

func projectOf(t *Task) string {
	if t == nil {
		return ""
	}
	return t.Project
}

func workflowOf(t *Task) string {
	if t == nil {
		return ""
	}
	return t.WorkflowID
}

func idOf(t *Task) string {
	if t == nil {
		return ""
	}
	return t.ID
}

func sortLiveContextItems(items []liveContextItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].rank != items[j].rank {
			return items[i].rank < items[j].rank
		}
		a, b := items[i].pointer, items[j].pointer
		if a == "" {
			a = items[i].ID + items[i].Key
		}
		if b == "" {
			b = items[j].ID + items[j].Key
		}
		return a < b
	})
}

func omissionList(reason string, pointers []string) []liveContextOmission {
	if len(pointers) == 0 {
		return nil
	}
	sort.Strings(pointers)
	shown := pointers
	if len(shown) > liveContextOmissionPointerCap {
		shown = append([]string(nil), pointers[:liveContextOmissionPointerCap]...)
	}
	return []liveContextOmission{{Count: len(pointers), Reason: reason, Pointers: shown}}
}

// renderLiveContextSnapshot packs scope, items, and omission facts under budget.
// Optional title prose is truncated to liveContextProseRunes on every item,
// including required predecessors. Required identity, status, and source/log
// pointers are always kept; only those necessary pointers may push a required
// item past liveContextSnapshotBudget. Every non-required item, including the
// first, is omitted once admitting it would pass the budget; those drops are
// disclosed as count/reason/pointers. After the greedy keep loop, optional
// items are trimmed from the end until the final serialized snapshot (items
// plus omission metadata) fits, or only required items remain. Required-only
// payloads that still exceed budget are the documented exception.
func renderLiveContextSnapshot(scope liveContextScope, items []liveContextItem, omitted []liveContextOmission, budget int) string {
	type payload struct {
		Scope   liveContextScopeView  `json:"scope"`
		Items   []liveContextItem     `json:"items"`
		Omitted []liveContextOmission `json:"omitted,omitempty"`
	}
	pack := func(keep []liveContextItem, extra []liveContextOmission) (string, error) {
		all := append(append([]liveContextOmission(nil), omitted...), extra...)
		data, err := json.MarshalIndent(payload{Scope: scope.view(), Items: keep, Omitted: all}, "", "  ")
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	keep := make([]liveContextItem, 0, len(items))
	var budgetPtrs []string
	for _, it := range items {
		it.Title = truncateRunes(it.Title, liveContextProseRunes)
		trial := append(keep, it)
		raw, err := pack(trial, omissionList("budget", budgetPtrs))
		if err != nil {
			return "（序列化队列失败）"
		}
		if budget > 0 && len(raw) > budget && !it.Required {
			budgetPtrs = append(budgetPtrs, it.pointer)
			continue
		}
		keep = trial
	}
	raw, err := pack(keep, omissionList("budget", budgetPtrs))
	if err != nil {
		return "（序列化队列失败）"
	}
	for budget > 0 && len(raw) > budget {
		drop := -1
		for i := len(keep) - 1; i >= 0; i-- {
			if !keep[i].Required {
				drop = i
				break
			}
		}
		if drop < 0 {
			break
		}
		budgetPtrs = append(budgetPtrs, keep[drop].pointer)
		keep = append(keep[:drop], keep[drop+1:]...)
		raw, err = pack(keep, omissionList("budget", budgetPtrs))
		if err != nil {
			return "（序列化队列失败）"
		}
	}
	return raw
}

func boundProgressReport(report map[string]any) map[string]any {
	if report == nil {
		return nil
	}
	if fmtKind, _ := report["format"].(string); fmtKind == "raw" {
		raw, _ := report["raw"].(string)
		return map[string]any{"format": "raw", "raw": truncateRunes(raw, liveContextProseRunes)}
	}
	out := map[string]any{}
	for _, k := range []string{"goal", "done", "in_progress", "remaining", "blockers", "key_files", "next_prompt"} {
		if v, ok := report[k]; ok {
			out[k] = boundAny(v, liveContextProseRunes)
		}
	}
	if len(out) == 0 {
		data, err := json.Marshal(report)
		if err != nil {
			return map[string]any{"format": "raw", "raw": ""}
		}
		return map[string]any{"format": "raw", "raw": truncateRunes(string(data), liveContextProseRunes)}
	}
	return out
}

func boundAny(v any, maxRunes int) any {
	if maxRunes < 1 {
		maxRunes = 1
	}
	child := maxRunes / 2
	if child < 1 {
		child = 1
	}
	switch x := v.(type) {
	case string:
		return truncateRunes(x, maxRunes)
	case []any:
		if len(x) > 8 {
			x = x[:8]
		}
		out := make([]any, len(x))
		for i, el := range x {
			out[i] = boundAny(el, child)
		}
		return out
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) > 8 {
			keys = keys[:8]
		}
		out := make(map[string]any, len(keys))
		for _, k := range keys {
			out[k] = boundAny(x[k], child)
		}
		return out
	default:
		return x
	}
}

// ---- 键名生成 ----

var slugRe = regexp.MustCompile(`[^a-z0-9\p{Han}]+`)

// progressSlug 从标题/目录生成人类可读的进度键。
func progressSlug(hint string) string {
	s := slugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(hint)), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "session"
	}
	r := []rune(s)
	if len(r) > 24 {
		s = string(r[:24])
	}
	return s + time.Now().Format("-0102-1504")
}
