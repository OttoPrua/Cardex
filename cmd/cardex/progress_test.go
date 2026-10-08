package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

type liveContextPayload struct {
	Scope struct {
		TaskID     string   `json:"task_id"`
		Project    string   `json:"project"`
		WorkflowID string   `json:"workflow_id"`
		Required   []string `json:"required"`
		Unscoped   bool     `json:"unscoped"`
	} `json:"scope"`
	Items []struct {
		ID       string         `json:"id"`
		Key      string         `json:"key"`
		Title    string         `json:"title"`
		Status   string         `json:"status"`
		Project  string         `json:"project"`
		Required bool           `json:"required"`
		Dir      string         `json:"dir"`
		Log      string         `json:"log"`
		Source   string         `json:"source"`
		Report   map[string]any `json:"report"`
	} `json:"items"`
	Omitted []struct {
		Count    int      `json:"count"`
		Reason   string   `json:"reason"`
		Pointers []string `json:"pointers"`
	} `json:"omitted"`
}

func parseLiveContext(t *testing.T, raw string) liveContextPayload {
	t.Helper()
	var p liveContextPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("live context is not JSON: %v\n%s", err, raw)
	}
	return p
}

func saveCoordTask(t *testing.T, root string, tk *Task) *Task {
	t.Helper()
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestInjectLiveContextScopedKeepsCrossProjectPredAndDisclosesOmissions(t *testing.T) {
	root := testRoot(t)
	if err := saveConfig(root, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	cfg := workflowTestCfg(t, root)
	coordDir := filepath.Join(root, "work", "alpha")
	otherDir := filepath.Join(root, "work", "alpha-extra") // directory-prefix trap
	crossDir := filepath.Join(root, "work", "beta")

	cross := newTask(root, cfg, typeSequence, "cross-project predecessor", crossDir, []string{"pred work"}, 9)
	cross.ID = "t-cross-pred"
	cross.Project = "beta"
	cross.Status = statusHeld
	saveCoordTask(t, root, cross)

	coord := newTask(root, cfg, typeCoordinate, "coordinate alpha", coordDir, []string{"{{QUEUE}}\n{{PROGRESS}}"}, 8)
	coord.ID = "t-coord-alpha"
	coord.Project = "alpha"
	coord.WorkflowID = "wf-alpha"
	coord.DependsOn = []string{cross.ID}
	saveCoordTask(t, root, coord)

	sib := newTask(root, cfg, typeSequence, "same project sibling", coordDir, []string{"sib"}, 5)
	sib.ID = "t-alpha-sib"
	sib.Project = "alpha"
	sib.WorkflowID = "wf-alpha"
	saveCoordTask(t, root, sib)

	heuristic := newTask(root, cfg, typeSequence, "heuristic-only same dir prefix", otherDir, []string{"should not be sole authority"}, 5)
	heuristic.ID = "t-heuristic-dir"
	heuristic.Project = "gamma"
	saveCoordTask(t, root, heuristic)

	for i := 0; i < 18; i++ {
		tk := newTask(root, cfg, typeSequence, "in-scope filler "+strings.Repeat("x", 40), coordDir, []string{"f"}, 3)
		tk.Project = "alpha"
		tk.WorkflowID = "wf-alpha"
		saveCoordTask(t, root, tk)
	}
	for i := 0; i < 8; i++ {
		tk := newTask(root, cfg, typeSequence, "out-of-scope other "+strings.Repeat("y", 40), filepath.Join(root, "work", "other"), []string{"o"}, 3)
		tk.Project = "other"
		tk.WorkflowID = "wf-other"
		saveCoordTask(t, root, tk)
		rep := map[string]any{"format": "raw", "raw": strings.Repeat("PROGRESS-OUT-"+tk.ID+" ", 400)}
		if err := saveProgress(root, &ProgressEntry{Key: tk.ID, Title: tk.Title, Dir: tk.Dir, Report: rep}); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveProgress(root, &ProgressEntry{
		Key: sib.ID, Title: sib.Title, Dir: sib.Dir,
		Report: map[string]any{"goal": "keep sibling", "done": "started", "raw_should_drop": strings.Repeat("z", 2000)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := saveProgress(root, &ProgressEntry{
		Key: cross.ID, Title: cross.Title, Dir: cross.Dir,
		Report: map[string]any{"format": "raw", "raw": "cross predecessor still held; see " + taskLogPath(root, cross.ID)},
	}); err != nil {
		t.Fatal(err)
	}

	queueRaw := injectLiveContext(root, coord.ID, "{{QUEUE}}")
	progRaw := injectLiveContext(root, coord.ID, "{{PROGRESS}}")
	if queueRaw == "{{QUEUE}}" || progRaw == "{{PROGRESS}}" {
		t.Fatal("placeholders were not substituted")
	}
	if len(queueRaw) > liveContextSnapshotBudget*2 {
		t.Fatalf("queue snapshot far over modest budget: %d", len(queueRaw))
	}
	q := parseLiveContext(t, queueRaw)
	p := parseLiveContext(t, progRaw)
	if q.Scope.Unscoped || q.Scope.Project != "alpha" || q.Scope.WorkflowID != "wf-alpha" {
		t.Fatalf("queue scope=%+v", q.Scope)
	}
	if len(q.Scope.Required) != 1 || q.Scope.Required[0] != cross.ID {
		t.Fatalf("required=%v", q.Scope.Required)
	}
	ids := map[string]bool{}
	crossSeen := false
	for _, it := range q.Items {
		ids[it.ID] = true
		if it.ID == heuristic.ID {
			t.Fatal("directory-prefix heuristic leaked into scoped queue")
		}
		if it.ID == cross.ID {
			crossSeen = true
			if !it.Required || it.Log == "" {
				t.Fatalf("cross-project predecessor missing required flag or log pointer: %+v", it)
			}
		}
	}
	if !crossSeen {
		t.Fatal("required cross-project predecessor omitted from queue")
	}
	if ids[heuristic.ID] {
		t.Fatal("heuristic-only task was treated as in-scope")
	}
	if len(q.Omitted) == 0 {
		t.Fatalf("scoped queue silently dropped out-of-scope rows:\n%s", queueRaw)
	}
	omittedIDs := strings.Join(q.Omitted[0].Pointers, ",")
	_ = omittedIDs
	foundOut := false
	for _, om := range q.Omitted {
		if om.Count <= 0 || om.Reason == "" || len(om.Pointers) == 0 {
			t.Fatalf("omission missing count/reason/pointers: %+v", om)
		}
		if om.Reason == "out_of_scope" {
			foundOut = true
		}
	}
	if !foundOut {
		t.Fatalf("expected out_of_scope omission, got %+v", q.Omitted)
	}

	progIDs := map[string]bool{}
	for _, it := range p.Items {
		progIDs[it.Key] = true
		if it.Key == heuristic.ID || strings.HasPrefix(it.Key, "t") && it.Project == "other" {
			t.Fatalf("out-of-scope progress leaked: %+v", it)
		}
		if it.Report != nil {
			if raw, ok := it.Report["raw"].(string); ok && strings.Count(raw, "PROGRESS-OUT-") > 0 {
				t.Fatal("out-of-scope progress prose leaked into scoped snapshot")
			}
		}
	}
	if !progIDs[sib.ID] && !progIDs[cross.ID] {
		t.Fatalf("scoped progress lost in-scope reports: %+v", p.Items)
	}
	if p.Scope.Unscoped {
		t.Fatal("progress snapshot marked unscoped")
	}

	dumpScratch(t, "coord-prompt-1.txt", queueRaw+"\n---\n"+progRaw)
	againQ := injectLiveContext(root, coord.ID, "{{QUEUE}}")
	againP := injectLiveContext(root, coord.ID, "{{PROGRESS}}")
	if againQ != queueRaw || againP != progRaw {
		t.Fatal("second live-context render did not match")
	}
	dumpScratch(t, "coord-prompt-2.txt", againQ+"\n---\n"+againP)
}

func TestInjectLiveContextLegacyUnscopedIsExplicitAndBounded(t *testing.T) {
	root := testRoot(t)
	if err := saveConfig(root, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	cfg := workflowTestCfg(t, root)
	coord := newTask(root, cfg, typeCoordinate, "legacy unscoped", filepath.Join(root, "work"), []string{"{{QUEUE}}"}, 5)
	coord.ID = "t-legacy-coord"
	saveCoordTask(t, root, coord)
	for i := 0; i < 30; i++ {
		tk := newTask(root, cfg, typeSequence, "legacy filler "+strings.Repeat("n", 80), filepath.Join(root, "work"), []string{"x"}, 3)
		saveCoordTask(t, root, tk)
		if err := saveProgress(root, &ProgressEntry{
			Key: tk.ID, Title: tk.Title, Dir: tk.Dir,
			Report: map[string]any{"format": "raw", "raw": strings.Repeat("LEGACY-PROSE-"+tk.ID+" ", 200)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	raw := injectLiveContext(root, coord.ID, "{{QUEUE}}")
	p := parseLiveContext(t, raw)
	if !p.Scope.Unscoped {
		t.Fatalf("legacy coordination must be explicit unscoped: %+v", p.Scope)
	}
	if len(raw) > liveContextSnapshotBudget+2048 {
		t.Fatalf("unscoped snapshot not bounded: %d", len(raw))
	}
	if len(p.Items) >= 30 && len(p.Omitted) == 0 {
		t.Fatal("unscoped dump included every row without omission disclosure")
	}
	if len(p.Omitted) > 0 {
		for _, om := range p.Omitted {
			if om.Count <= 0 || om.Reason == "" || len(om.Pointers) == 0 {
				t.Fatalf("unscoped omission incomplete: %+v", om)
			}
		}
	}
	prog := injectLiveContext(root, coord.ID, "{{PROGRESS}}")
	pp := parseLiveContext(t, prog)
	if !pp.Scope.Unscoped {
		t.Fatal("legacy progress must stay explicit unscoped")
	}
	for _, it := range pp.Items {
		if it.Report == nil {
			continue
		}
		if raw, ok := it.Report["raw"].(string); ok {
			if len([]rune(raw)) > liveContextProseRunes+10 {
				t.Fatalf("prose fallback was not bounded: %d runes", len([]rune(raw)))
			}
		}
	}
}

func TestInjectLiveContextHugeFirstLegacyItemAndNestedMapStayBounded(t *testing.T) {
	root := testRoot(t)
	if err := saveConfig(root, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	cfg := workflowTestCfg(t, root)
	work := filepath.Join(root, "work")

	legacy := saveCoordTask(t, root, func() *Task {
		tk := newTask(root, cfg, typeCoordinate, "legacy unscoped huge", work, []string{"{{QUEUE}}\n{{PROGRESS}}"}, 5)
		tk.ID = "t-legacy-huge"
		return tk
	}())

	hugeTitle := strings.Repeat("HUGE-LEGACY-TITLE-", 3000)
	nestedBlob := strings.Repeat("NESTED-MAP-BLOB-", 2500)
	innerBlob := strings.Repeat("INNER-PAYLOAD-", 2500)
	huge := newTask(root, cfg, typeSequence, hugeTitle, work, []string{"x"}, 3)
	huge.ID = "t-aaa-huge"
	saveCoordTask(t, root, huge)
	small := newTask(root, cfg, typeSequence, "small later row", work, []string{"y"}, 3)
	small.ID = "t-zzz-small"
	saveCoordTask(t, root, small)
	if err := saveProgress(root, &ProgressEntry{
		Key: huge.ID, Title: "legacy nested map report", Dir: huge.Dir,
		Report: map[string]any{
			"goal": "legacy nested map",
			"done": map[string]any{
				"blob": nestedBlob,
				"inner": map[string]any{
					"payload": innerBlob,
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := saveProgress(root, &ProgressEntry{
		Key: small.ID, Title: small.Title, Dir: small.Dir,
		Report: map[string]any{"goal": "later small", "done": "ok"},
	}); err != nil {
		t.Fatal(err)
	}

	queueRaw := injectLiveContext(root, legacy.ID, "{{QUEUE}}")
	progRaw := injectLiveContext(root, legacy.ID, "{{PROGRESS}}")
	if len(queueRaw) > liveContextSnapshotBudget {
		t.Fatalf("legacy queue with huge first item exceeded budget: %d", len(queueRaw))
	}
	if len(progRaw) > liveContextSnapshotBudget {
		t.Fatalf("legacy progress with nested map exceeded budget: %d", len(progRaw))
	}
	if strings.Count(queueRaw, "HUGE-LEGACY-TITLE-") >= 3000 {
		t.Fatal("huge first legacy title was admitted unbounded")
	}
	if strings.Count(progRaw, "NESTED-MAP-BLOB-") >= 2500 {
		t.Fatal("nested map blob was admitted unbounded")
	}
	if strings.Count(progRaw, "INNER-PAYLOAD-") >= 2500 {
		t.Fatal("nested map inner payload was admitted unbounded")
	}

	q := parseLiveContext(t, queueRaw)
	if !q.Scope.Unscoped {
		t.Fatalf("legacy queue must stay explicit unscoped: %+v", q.Scope)
	}
	seenQ := map[string]bool{}
	for _, it := range q.Items {
		seenQ[it.ID] = true
		if len([]rune(it.Title)) > liveContextProseRunes+10 {
			t.Fatalf("queue title prose unbounded: id=%s runes=%d", it.ID, len([]rune(it.Title)))
		}
	}
	if !seenQ[small.ID] {
		t.Fatal("later in-budget legacy queue row was dropped with the huge first item")
	}
	for _, it := range q.Items {
		if it.ID == huge.ID && strings.Count(it.Title, "HUGE-LEGACY-TITLE-") >= 3000 {
			t.Fatal("huge first legacy title was kept in full")
		}
	}
	for _, om := range q.Omitted {
		if om.Count <= 0 || om.Reason == "" || len(om.Pointers) == 0 {
			t.Fatalf("queue omission incomplete: %+v", om)
		}
	}

	p := parseLiveContext(t, progRaw)
	if !p.Scope.Unscoped {
		t.Fatal("legacy progress must stay explicit unscoped")
	}
	foundHugeProg := false
	for _, it := range p.Items {
		if it.Key != huge.ID {
			continue
		}
		foundHugeProg = true
		if it.Source == "" {
			t.Fatal("bounded nested-map progress lost its source pointer")
		}
		done, _ := it.Report["done"].(map[string]any)
		if done == nil {
			t.Fatalf("nested map was not preserved as a bounded object: %+v", it.Report)
		}
		blob, _ := done["blob"].(string)
		if len([]rune(blob)) > liveContextProseRunes {
			t.Fatalf("nested map blob still over prose bound: %d runes", len([]rune(blob)))
		}
		inner, _ := done["inner"].(map[string]any)
		if inner != nil {
			payload, _ := inner["payload"].(string)
			if len([]rune(payload)) > liveContextProseRunes {
				t.Fatalf("nested map inner payload still over prose bound: %d runes", len([]rune(payload)))
			}
		}
	}
	if !foundHugeProg {
		t.Fatal("nested-map progress was dropped instead of bounded in place")
	}

	dumpScratch(t, "coord-prompt-huge-first.txt", queueRaw+"\n---\n"+progRaw)

	reqTitle := strings.Repeat("HUGE-REQUIRED-TITLE-", 3000)
	pred := newTask(root, cfg, typeSequence, reqTitle, filepath.Join(root, "work", "beta"), []string{"pred"}, 9)
	pred.ID = "t-req-pred"
	pred.Project = "beta"
	pred.Status = statusHeld
	saveCoordTask(t, root, pred)
	sib := newTask(root, cfg, typeSequence, strings.Repeat("HUGE-SIBLING-TITLE-", 3000), filepath.Join(root, "work", "alpha"), []string{"sib"}, 3)
	sib.ID = "t-aaa-sib"
	sib.Project = "alpha"
	saveCoordTask(t, root, sib)
	coord := newTask(root, cfg, typeCoordinate, "scoped required exception", filepath.Join(root, "work", "alpha"), []string{"{{QUEUE}}"}, 8)
	coord.ID = "t-coord-req"
	coord.Project = "alpha"
	coord.DependsOn = []string{pred.ID}
	saveCoordTask(t, root, coord)

	reqRaw := injectLiveContext(root, coord.ID, "{{QUEUE}}")
	rq := parseLiveContext(t, reqRaw)
	reqSeen := false
	for _, it := range rq.Items {
		if len([]rune(it.Title)) > liveContextProseRunes+10 {
			t.Fatalf("optional title prose unbounded on %s: %d runes", it.ID, len([]rune(it.Title)))
		}
		if it.ID == pred.ID {
			reqSeen = true
			if !it.Required || it.Log == "" {
				t.Fatalf("required predecessor lost flag or log pointer: %+v", it)
			}
			if it.Status != statusHeld {
				t.Fatalf("required predecessor lost status: %+v", it)
			}
			if !strings.Contains(it.Log, pred.ID) {
				t.Fatalf("required log pointer missing id: %+v", it)
			}
		}
	}
	if !reqSeen {
		t.Fatal("required predecessor pointer was dropped to meet the 8192-byte budget")
	}
	if strings.Count(reqRaw, "HUGE-REQUIRED-TITLE-") >= 3000 {
		t.Fatal("oversized optional title expanded a required item")
	}
	if strings.Count(reqRaw, "HUGE-SIBLING-TITLE-") >= 3000 {
		t.Fatalf("oversized sibling title %s expanded the required snapshot", sib.ID)
	}
	if len(reqRaw) > liveContextSnapshotBudget {
		t.Fatalf("optional title prose expanded required snapshot past budget: %d", len(reqRaw))
	}
	dumpScratch(t, "coord-prompt-required-exception.txt", reqRaw)
}

func TestInjectLiveContextCounterexampleHeuristicOnlyScoping(t *testing.T) {
	root := testRoot(t)
	if err := saveConfig(root, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	cfg := workflowTestCfg(t, root)
	base := filepath.Join(root, "Projects", "Cardex")
	coord := newTask(root, cfg, typeCoordinate, "coord", base, []string{"{{QUEUE}}"}, 5)
	coord.ID = "t-coord"
	coord.Project = "cardex"
	saveCoordTask(t, root, coord)
	trap := newTask(root, cfg, typeSequence, "same prefix other project", filepath.Join(root, "Projects", "Cardex-worktrees"), []string{"no"}, 3)
	trap.ID = "t-dir-trap"
	trap.Project = "other-tree"
	saveCoordTask(t, root, trap)
	raw := injectLiveContext(root, coord.ID, "{{QUEUE}}")
	p := parseLiveContext(t, raw)
	for _, it := range p.Items {
		if it.ID == trap.ID {
			t.Fatal("directory prefix must not be the sole scoping authority")
		}
	}
}
