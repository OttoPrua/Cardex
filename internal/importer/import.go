package importer

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"cardex/internal/model"
	"cardex/internal/notify"
	"cardex/internal/store"
)

const legacySourceEvent = "legacy_source"

func importHome(ctx context.Context, v2Home, v1Home string, dryRun bool) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	info, err := os.Stat(v1Home)
	if err != nil {
		return Report{}, err
	}
	if !info.IsDir() {
		return Report{}, fmt.Errorf("v1 home %s is not a directory", v1Home)
	}
	files, err := listV1(v1Home)
	if err != nil {
		return Report{}, err
	}
	report := Report{DryRun: dryRun, Counts: map[string]int{}}
	type row struct {
		rel string
		id  string
		rec *v1Record
		pl  placement
	}
	var rows []row
	seen := map[string]bool{}
	for _, rel := range files {
		rec, perr := readRecord(filepath.Join(v1Home, filepath.FromSlash(rel)))
		pl := place(rel, rec, perr)
		id := cardID(rel, "")
		if rec != nil {
			id = cardID(rel, rec.ID)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		report.Counts["v1:"+pl.V1Status]++
		report.Counts["v2:"+pl.V2Place]++
		if pl.Unparsed != "" {
			report.Unparsed = append(report.Unparsed, pl.Unparsed)
		}
		rows = append(rows, row{rel: rel, id: id, rec: rec, pl: pl})
	}
	sort.Strings(report.Unparsed)
	var tasks []importedTask
	for _, row := range rows {
		if row.rec == nil || strings.TrimSpace(row.rec.ID) == "" {
			continue
		}
		tasks = append(tasks, importedTask{ID: strings.TrimSpace(row.rec.ID), Project: projectKey(row.rec)})
	}
	notes, receivers := mapSubscriptions(v1Home, tasks, existingReceiverIDs(v2Home))
	report.Notes = notes
	if dryRun {
		return report, nil
	}
	if strings.TrimSpace(v2Home) == "" {
		return Report{}, errors.New("v2 home is required")
	}
	same, err := sameDirectory(v2Home, v1Home)
	if err != nil {
		return Report{}, err
	}
	if same {
		return Report{}, errors.New("v2 home and v1 home are the same directory")
	}
	db, err := store.Open(filepath.Join(v2Home, "cardex.db"))
	if err != nil {
		return Report{}, err
	}
	defer db.Close()
	repos := map[string]string{}
	for _, row := range rows {
		if row.pl.RepoPath == "" {
			continue
		}
		if _, ok := repos[row.pl.ProjectKey]; ok {
			continue
		}
		repos[row.pl.ProjectKey] = row.pl.RepoPath
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		if err := writeRow(ctx, db, v1Home, row.id, row.pl, repos[row.pl.ProjectKey]); err != nil {
			return Report{}, err
		}
	}
	if len(receivers) > 0 {
		skipped, err := notify.ImportReceivers(v2Home, receivers)
		if err != nil {
			return Report{}, err
		}
		for _, id := range skipped {
			report.Notes = append(report.Notes, "receiver "+id+": already exists")
		}
	}
	return report, nil
}

func listV1(root string) ([]string, error) {
	var out []string
	for _, dir := range []string{"tasks", "archive"} {
		names, err := jsonRecords(filepath.Join(root, dir))
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			out = append(out, dir+"/"+name)
		}
	}
	names, err := jsonRecords(filepath.Join(root, "workflows"))
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if strings.HasSuffix(name, ".progress.json") {
			continue
		}
		if knownWorkflowSidecar(filepath.Join(root, "workflows", name), name) {
			continue
		}
		out = append(out, "workflows/"+name)
	}
	sort.Strings(out)
	return out, nil
}

func jsonRecords(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}

// knownWorkflowSidecar matches v1 scanWorkflows: progress files are skipped by
// the caller. design-receipt and retained-preimage files are sidecars only when
// they carry that shape and none of the workflow record fields.
func knownWorkflowSidecar(path, name string) bool {
	if !strings.HasSuffix(name, ".design-receipt.json") && !strings.HasSuffix(name, ".retained-preimage-hashes.json") {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false
	}
	for _, key := range workflowRecordKeys {
		for field := range fields {
			if strings.EqualFold(field, key) {
				return false
			}
		}
	}
	hashOK := func(value string, size int) bool {
		b, err := hex.DecodeString(value)
		return err == nil && len(b) == size
	}
	if strings.HasSuffix(name, ".design-receipt.json") {
		var receipt struct {
			Role          string `json:"role"`
			Runner        string `json:"runner"`
			ActualRunner  string `json:"actual_runner"`
			Identity      string `json:"identity"`
			SessionID     string `json:"session_id"`
			Digest        string `json:"digest"`
			InputIdentity string `json:"input_identity"`
			ReadOnly      *bool  `json:"read_only"`
			Status        string `json:"status"`
			ResultPath    string `json:"result_path"`
		}
		if json.Unmarshal(raw, &receipt) != nil {
			return false
		}
		return receipt.Role == "design" && receipt.ReadOnly != nil && *receipt.ReadOnly && receipt.Status == "completed" &&
			receipt.Runner != "" && receipt.ActualRunner != "" && receipt.Identity != "" && receipt.SessionID != "" && receipt.ResultPath != "" &&
			hashOK(receipt.Digest, 32) && strings.TrimSpace(receipt.InputIdentity) != ""
	}
	if len(fields) != 3 {
		return false
	}
	var preimage struct {
		Meaning string            `json:"meaning"`
		Head    string            `json:"head"`
		Hashes  map[string]string `json:"hashes"`
	}
	if json.Unmarshal(raw, &preimage) != nil || strings.TrimSpace(preimage.Meaning) == "" || !hashOK(preimage.Head, 20) || len(preimage.Hashes) == 0 {
		return false
	}
	for item, value := range preimage.Hashes {
		if strings.TrimSpace(item) == "" || !hashOK(value, 32) {
			return false
		}
	}
	return true
}

var workflowRecordKeys = []string{
	"schema", "id", "mode", "module_id", "goal_id", "goal", "parent_id", "repo", "worktree", "write_domain",
	"writer_engine", "writer_work_class", "writer_model", "writer_route_class", "reviewer_engine", "effect_gates",
	"current_round", "max_rounds", "writer_task_id", "reviewer_task_id", "integration_task_id", "terminal_criteria",
	"candidate", "review", "progress", "material_notify", "design_lineage", "design_session", "manager_session",
	"owner_design_entry", "design_task_id", "direction_task_ids", "native_goal_task_ids", "acceptance_task_id",
	"acceptance_coverage", "goal_completed", "extra_round_grants", "extra_round_consumptions", "created_at", "updated_at",
}

func readRecord(path string) (*v1Record, string) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err.Error()
	}
	if !json.Valid(body) {
		return nil, "invalid json"
	}
	var rec v1Record
	if err := json.Unmarshal(body, &rec); err != nil {
		return nil, err.Error()
	}
	return &rec, ""
}

func writeRow(ctx context.Context, db *store.Store, v1Home, id string, pl placement, repo string) error {
	if _, err := db.GetCard(ctx, id); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	proj := projectID(pl.ProjectKey)
	if _, err := db.GetProject(ctx, proj); errors.Is(err, sql.ErrNoRows) {
		if repo == "" {
			repo = v1Home
		}
		p := model.Project{
			ID:           proj,
			RepoPath:     repo,
			WorktreeRoot: filepath.Join(repo, "worktrees", "cardex"),
			TestCmd:      "go test ./...",
			DesignDir:    "docs/design/",
		}
		if err := db.CreateProject(ctx, &p); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	objID := objectiveID(pl.ProjectKey)
	if _, err := db.GetObjective(ctx, objID); errors.Is(err, sql.ErrNoRows) {
		o := model.Objective{
			ID:                objID,
			Project:           proj,
			Title:             importObjectiveTitle,
			Mode:              model.ModeManual,
			FreezeMode:        model.FreezeSafe,
			Status:            model.ObjectiveActive,
			IntegrationBranch: "cardex/" + objID + "/main",
		}
		if err := db.CreateObjective(ctx, &o); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	card := model.Card{
		ID:        id,
		Objective: objID,
		Kind:      pl.Kind,
		Title:     pl.Title,
		Body:      pl.Body,
		Deps:      pl.Deps,
		Status:    pl.Status,
		Legacy:    pl.Legacy,
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		return err
	}
	if pl.Legacy.Status == "owner_choice" {
		if err := writeOwnerQuestion(ctx, db, objID, id, pl.Title); err != nil {
			return err
		}
	}
	if err := writeLegacySource(ctx, db, v1Home, id, pl.Legacy.SourcePath); err != nil {
		return err
	}
	if pl.Hint.empty() {
		return nil
	}
	return writeHint(ctx, db, id, pl.Hint)
}

func writeOwnerQuestion(ctx context.Context, db *store.Store, objective, cardID, title string) error {
	nodeID := "n-" + cardID
	if _, err := db.GetNode(ctx, nodeID); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	text := strings.TrimSpace(title)
	if text == "" {
		text = cardID
	}
	return db.CreateNode(ctx, &model.DesignNode{
		ID:        nodeID,
		Objective: objective,
		Layer:     model.LayerDirection,
		DocPath:   "docs/design/import.md",
		Status:    model.NodeDrafting,
		OpenQuestions: model.OpenQuestionList{{
			ID:     "oq-" + cardID,
			Text:   text,
			Type:   model.OQOwner,
			Status: model.OQOpen,
		}},
	})
}

func writeLegacySource(ctx context.Context, db *store.Store, v1Home, cardID, rel string) error {
	entity := "card:" + cardID
	existing, err := db.ListEvents(ctx, entity)
	if err != nil {
		return err
	}
	for _, ev := range existing {
		if ev.Type == legacySourceEvent {
			return nil
		}
	}
	home, path := legacySourceLocation(v1Home, rel)
	data, err := json.Marshal(struct {
		Home string `json:"home"`
		Path string `json:"path"`
	}{Home: home, Path: path})
	if err != nil {
		return err
	}
	return db.AppendEvent(ctx, &model.Event{
		Entity: entity,
		Type:   legacySourceEvent,
		Data:   data,
	})
}

func legacySourceLocation(v1Home, rel string) (home, path string) {
	home = v1Home
	if abs, err := filepath.Abs(v1Home); err == nil {
		home = abs
	}
	return home, filepath.Join(home, filepath.FromSlash(rel))
}

func sameDirectory(a, b string) (bool, error) {
	ia, err := os.Stat(a)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	ib, err := os.Stat(b)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if !ia.IsDir() || !ib.IsDir() {
		return false, nil
	}
	return os.SameFile(ia, ib), nil
}

func writeHint(ctx context.Context, db *store.Store, cardID string, hint routeHint) error {
	entity := "card:" + cardID
	existing, err := db.ListEvents(ctx, entity)
	if err != nil {
		return err
	}
	for _, ev := range existing {
		if ev.Type == "route_hint" {
			return nil
		}
	}
	data, err := json.Marshal(hint)
	if err != nil {
		return err
	}
	return db.AppendEvent(ctx, &model.Event{
		Entity: entity,
		Type:   "route_hint",
		Data:   data,
	})
}
