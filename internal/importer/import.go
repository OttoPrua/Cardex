package importer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"cardex/internal/model"
	"cardex/internal/store"
)

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
	if dryRun {
		return report, nil
	}
	if strings.TrimSpace(v2Home) == "" {
		return Report{}, errors.New("v2 home is required")
	}
	db, err := store.Open(filepath.Join(v2Home, "cardex.db"))
	if err != nil {
		return Report{}, err
	}
	defer db.Close()
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		if err := writeRow(ctx, db, v1Home, row.id, row.pl); err != nil {
			return Report{}, err
		}
	}
	return report, nil
}

func listV1(root string) ([]string, error) {
	var out []string
	for _, dir := range []string{"tasks", "archive", "workflows"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, ent := range entries {
			if ent.IsDir() {
				continue
			}
			name := ent.Name()
			if !strings.HasSuffix(strings.ToLower(name), ".json") {
				continue
			}
			out = append(out, dir+"/"+name)
		}
	}
	sort.Strings(out)
	return out, nil
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

func writeRow(ctx context.Context, db *store.Store, v1Home, id string, pl placement) error {
	if _, err := db.GetCard(ctx, id); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	proj := projectID(pl.ProjectKey)
	if _, err := db.GetProject(ctx, proj); errors.Is(err, sql.ErrNoRows) {
		repo := pl.RepoPath
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
		Status:    pl.Status,
		Legacy:    pl.Legacy,
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		return err
	}
	if pl.Hint.empty() {
		return nil
	}
	return writeHint(ctx, db, id, pl.Hint)
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


