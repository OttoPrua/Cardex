package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/homeguard"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/positioning"
	"github.com/OttoPrua/Cardex/internal/quota"
	"github.com/OttoPrua/Cardex/internal/store"
)

const doctorUsagePurpose = "check the home, database, tools, and channel config, then print only problems and a repair suggestion for each"

type finding struct {
	problem    string
	suggestion string
}

func cmdDoctor(home string, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("doctor", doctorUsagePurpose, "no command arguments", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	for _, item := range collectDoctor(home) {
		fmt.Fprintf(stdout, "problem: %s\nsuggestion: %s\n", item.problem, item.suggestion)
	}
	return 0
}

func collectDoctor(home string) []finding {
	var out []finding
	if strings.HasPrefix(buildRevision(), "unknown") {
		out = append(out, finding{"binary has no commit revision, so the running version cannot be traced", "build from a git checkout, or pass -ldflags \"-X main.revision=$(git rev-parse --short HEAD)\""})
	}
	resolved, err := resolveHome(home)
	if err != nil {
		suggestion := "pass a writable --home"
		if homeguard.IsV1(err) {
			suggestion = "用 import-legacy 导入到新目录：" + homeguard.Usage
		}
		out = append(out, finding{err.Error(), suggestion})
		resolved = ""
	} else if problem, suggestion, bad := homeWritable(resolved); bad {
		out = append(out, finding{problem, suggestion})
	}
	var db *store.Store
	if resolved == "" {
		out = append(out, finding{"database cannot be opened because the home is unavailable", "pass a writable --home"})
		out = append(out, finding{"migration version cannot be read because the database is not open", "pass a writable --home so migrations can be read"})
	} else {
		opened, oerr := store.Open(absDB(resolved))
		if oerr != nil {
			out = append(out, finding{"database cannot be opened: " + oerr.Error(), "check that the home is a directory and cardex.db is not corrupt"})
			out = append(out, finding{"migration version cannot be read because the database did not open", "repair cardex.db or pass a new --home"})
		} else {
			db = opened
			defer db.Close()
			if _, verr := db.SchemaVersion(context.Background()); verr != nil {
				out = append(out, finding{"migration version cannot be read: " + verr.Error(), "open this home with the current cardex binary so the migration table can be read"})
			}
		}
	}
	out = append(out, toolFindings()...)
	out = append(out, channelFindings(db, resolved)...)
	return out
}

func absDB(home string) string {
	return filepath.Join(home, "cardex.db")
}

func homeWritable(home string) (problem, suggestion string, bad bool) {
	f, err := os.CreateTemp(home, ".doctor-write-*")
	if err != nil {
		return "home is not writable", "choose a --home the current user can write, or fix the directory permissions", true
	}
	name := f.Name()
	_ = f.Close()
	if err := os.Remove(name); err != nil {
		return "home write probe could not be removed", "check permissions on " + home, true
	}
	return "", "", false
}

func toolFindings() []finding {
	var out []finding
	out = append(out, lookupFinding("git", "install git and put it on PATH")...)
	out = append(out, lookupFinding("sandbox-exec", "on macOS this ships with the system; elsewhere install it or run doctor on macOS. doctor only looks it up and does not run it")...)
	for _, name := range []string{"grok", "codex", "claude", "cursor-agent", "kimi", "agy", "opencode"} {
		out = append(out, lookupFinding(name, "install "+name+" or add it to PATH. doctor only looks up the name and does not run it")...)
	}
	return out
}

func lookupFinding(name, suggestion string) []finding {
	if _, err := exec.LookPath(name); err != nil {
		return []finding{{name + " is not on PATH", suggestion}}
	}
	return nil
}

func channelFindings(db *store.Store, home string) []finding {
	var out []finding
	if db == nil {
		out = append(out, finding{"channel config cannot be read", "open a home whose database can be created"})
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
	pos, perr := loadPositioning(ctx, home)
	if perr != nil {
		out = append(out, finding{"positioning table cannot be read: " + perr.Error(), "open the home with positioning.Open and approve a proposal"})
		pos = nil
	}
	for _, tier := range model.AllTiers() {
		if !tierAvailable(channels, pos, tier) {
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

func loadPositioning(ctx context.Context, home string) ([]model.Positioning, error) {
	if home == "" {
		return nil, fmt.Errorf("home is unavailable")
	}
	h, err := positioning.Open(home)
	if err != nil {
		return nil, err
	}
	defer h.Close()
	return h.Current(ctx)
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
