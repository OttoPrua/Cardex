package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/homeguard"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/positioning"
	"github.com/OttoPrua/Cardex/internal/proc"
	"github.com/OttoPrua/Cardex/internal/quota"
	"github.com/OttoPrua/Cardex/internal/roots"
	"github.com/OttoPrua/Cardex/internal/store"
)

const doctorUsagePurpose = "inspect the home, database, tools, channel config, and storage volumes"

type finding struct {
	problem    string
	suggestion string
}

func cmdDoctor(home string, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("doctor", doctorUsagePurpose, "no command arguments", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	lines, items := collectDoctor(home)
	for _, line := range lines {
		fmt.Fprintln(stdout, line)
	}
	for _, item := range items {
		fmt.Fprintf(stdout, "problem: %s\nsuggestion: %s\n", item.problem, item.suggestion)
	}
	return 0
}

func collectDoctor(home string) ([]string, []finding) {
	var out []finding
	if strings.HasPrefix(buildRevision(), "unknown") {
		out = append(out, finding{"binary has no commit revision, so the running version cannot be traced", "build from a git checkout, or pass -ldflags \"-X main.revision=$(git rev-parse --short HEAD)\""})
	}
	resolved, err := homePath(home)
	if err == nil {
		err = homeguard.Check(resolved)
	}
	if err != nil {
		suggestion := "pass a writable --home"
		if homeguard.IsV1(err) {
			suggestion = "用 import-legacy 导入到新目录：" + homeguard.Usage
		}
		out = append(out, finding{err.Error(), suggestion})
		resolved = ""
	}
	var projects []model.Project
	var dbFindings []finding
	ctx := context.Background()
	if resolved == "" {
		out = append(out, finding{"database cannot be opened because the home is unavailable", "pass a writable --home"})
	} else {
		err = store.Inspect(absDB(resolved), func(db *store.Store) {
			if _, verr := db.SchemaVersion(ctx); verr != nil {
				dbFindings = append(dbFindings, finding{"migration version cannot be read: " + verr.Error(), "open this home with the current cardex binary so the migration table can be read"})
			}
			var perr error
			projects, perr = db.ListProjects(ctx)
			if perr != nil {
				dbFindings = append(dbFindings, finding{"project scratch configuration cannot be read: " + perr.Error(), "open this home with the current cardex binary and run doctor again"})
			}
			dbFindings = append(dbFindings, channelFindings(db, resolved, false)...)
		})
		if err != nil {
			out = append(out, finding{"database cannot be inspected: " + err.Error(), "check the database or rerun doctor when the home is ready"})
		}
	}
	if resolved == "" || err != nil {
		out = append(out, finding{"migration version cannot be read because the database was unavailable", "pass a home with an initialized database"})
		dbFindings = channelFindings(nil, resolved, false)
		projects = nil
	}
	out = append(out, toolFindings()...)
	out = append(out, dbFindings...)
	var storage []string
	if resolved != "" {
		var warnings []finding
		storage, warnings = storageReport(resolved, projects)
		out = append(out, warnings...)
	}
	return storage, out
}

func storageReport(home string, projects []model.Project) ([]string, []finding) {
	layout := roots.Load(home)
	locs := layout.Locations()
	for _, p := range projects {
		if strings.TrimSpace(p.ScratchDir) == "" {
			continue
		}
		dir, ok := roots.ResolveDir(home, p.ScratchDir)
		if !ok {
			continue
		}
		locs = append(locs, roots.Location{Name: "scratch:" + p.ID, Path: dir})
	}
	var lines []string
	var warnings []finding
	for _, loc := range locs {
		info, err := roots.Volume(loc.Path)
		if err != nil {
			lines = append(lines, fmt.Sprintf("storage: name=%s path=%s volume= error=%s", loc.Name, loc.Path, err.Error()))
			warnings = append(warnings, finding{
				loc.Name + " directory " + loc.Path + " volume cannot be read: " + err.Error(),
				"create the directory on a mounted volume or fix the path",
			})
			continue
		}
		lines = append(lines, fmt.Sprintf("storage: name=%s path=%s volume=%s free=%d", loc.Name, loc.Path, info.Mount, info.Free))
		if roots.BelowFreeFloor(info.Free, roots.FreeWarnBytes) {
			warnings = append(warnings, finding{
				fmt.Sprintf("%s directory %s on volume %s has %d bytes free, below %d", loc.Name, loc.Path, info.Mount, info.Free, roots.FreeWarnBytes),
				"free space on that volume or point the directory at another disk",
			})
		}
	}
	return lines, warnings
}

func absDB(home string) string {
	return filepath.Join(home, "cardex.db")
}

func toolFindings() []finding {
	var out []finding
	out = append(out, lookupFinding("git", "install git and put it on PATH")...)
	out = append(out, sandboxFindings()...)
	return out
}

func lookupFinding(name, suggestion string) []finding {
	if _, err := proc.LookPath(name, nil); err != nil {
		return []finding{{name + " is not on PATH", suggestion}}
	}
	return nil
}

func channelFindings(db *store.Store, home string, runProbes bool) []finding {
	var out []finding
	if db == nil {
		out = append(out, finding{"channel config cannot be read", "pass a home with an initialized database"})
		for _, tier := range model.AllTiers() {
			out = append(out, tierFinding(tier))
		}
		return out
	}
	ctx := context.Background()
	channels, err := db.ListChannels(ctx)
	if err != nil {
		out = append(out, finding{"channel config cannot be read: " + err.Error(), "check the channels table in cardex.db"})
		channels = nil
	}
	if err == nil {
		out = append(out, cliFindings(ctx, channels, runProbes)...)
	}
	h := positioning.Inspect(home, db)
	unapproved := approvalFindings(ctx, h)
	out = append(out, unapproved...)
	pos, perr := h.Current(ctx)
	if perr != nil {
		out = append(out, finding{"positioning table cannot be read: " + perr.Error(), "open the home with positioning.Open and approve a proposal"})
		pos = nil
	}
	// Before approval every tier is empty; the approval finding already says
	// what to do, so the three tier findings would only repeat it.
	for _, tier := range model.AllTiers() {
		if len(unapproved) == 0 && !tierAvailable(channels, pos, tier) {
			out = append(out, tierFinding(tier))
		}
	}
	for _, ch := range channels {
		if ch.Redline < 0 || ch.Redline > 1 {
			out = append(out, finding{
				fmt.Sprintf("channel %s redline %v is outside 0 through 1", ch.ID, ch.Redline),
				"run channel set --id " + ch.ID + " --redline with a value from 0 to 1 inclusive",
			})
		}
		for _, w := range ch.QuotaWindows {
			if !windowAccepted(w.Name, w.ResetRule) {
				out = append(out, finding{
					fmt.Sprintf("channel %s window %q is not accepted by the quota parser", ch.ID, w.Name),
					"use a window name of 5h, daily, weekly, or monthly",
				})
			}
		}
	}
	return out
}

func tierFinding(tier model.Tier) finding {
	return finding{
		fmt.Sprintf("tier %s has no healthy channel whose model the positioning table places on that tier", tier),
		"add a healthy channel and approve a positioning row whose tiers include " + string(tier),
	}
}

func tierAvailable(channels []model.Channel, pos []model.Positioning, tier model.Tier) bool {
	want := map[string]bool{}
	for _, row := range pos {
		for _, item := range row.Tiers {
			if item == tier && row.Model != "" {
				want[row.Model] = true
			}
		}
	}
	if len(want) == 0 {
		return false
	}
	for _, ch := range channels {
		if ch.Health != model.HealthOK {
			continue
		}
		for _, name := range ch.Models {
			if want[name] {
				return true
			}
		}
	}
	return false
}

// windowAccepted asks the quota ledger whether it keeps this window.
// The parser drops unknown names and accepts every reset rule (unknown rules become rolling).
func windowAccepted(name, rule string) bool {
	led := quota.New()
	led.Configure(model.Channel{
		ID: "doctor-probe",
		QuotaWindows: []model.QuotaWindow{{
			Name:        name,
			ResetRule:   rule,
			LimitTokens: 1,
			ResetAt:     time.Unix(0, 0).UTC(),
		}},
	})
	est, err := led.Remaining(context.Background(), "doctor-probe")
	return err == nil && !est.Unknown
}

// approvalFindings reports a table that is shown but not routed: the scheduler
// dispatches only from an approved positioning table.
func approvalFindings(ctx context.Context, h *positioning.Home) []finding {
	ok, err := h.Approved(ctx)
	if err != nil || ok {
		return nil
	}
	hint := "run cardex positioning propose, then cardex positioning approve <id>"
	if pending, perr := h.Pending(); perr == nil && len(pending) > 0 {
		hint = "approve the waiting proposal: cardex positioning approve " + pending[0].ID
	}
	return []finding{{"no positioning table is approved, so no card is dispatched", hint}}
}
