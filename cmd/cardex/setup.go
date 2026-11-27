package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/positioning"
	"github.com/OttoPrua/Cardex/internal/proc"
)

// setupModel is one recommended model for a runner and the tiers it covers.
type setupModel struct {
	name  string
	tiers []model.Tier
	caps  []string
}

// setupPlan is the recommended channel for one runner. Models and tiers are a
// starting point for beginners; advanced users edit them with channel set and
// positioning propose --file.
type setupPlan struct {
	runner      model.RunnerName
	concurrency int
	models      []setupModel
}

var (
	t1   = []model.Tier{model.TierT1}
	t2   = []model.Tier{model.TierT2}
	t3   = []model.Tier{model.TierT3}
	t12  = []model.Tier{model.TierT1, model.TierT2}
	t23  = []model.Tier{model.TierT2, model.TierT3}
	t123 = []model.Tier{model.TierT1, model.TierT2, model.TierT3}
)

// setupPlans: one subscription still fills T1-T3 by splitting the provider's
// stronger and lighter models. Order is the default preference order.
var setupPlans = []setupPlan{
	{model.RunnerCodex, 8, []setupModel{{"gpt-6.2-astra", t1, nil}, {"gpt-6.2-sol", t23, nil}}},
	{model.RunnerGrok, 16, []setupModel{{"grok-4.7", t12, []string{"goal_mode"}}, {"grok-4.6", t3, nil}}},
	{model.RunnerClaude, 6, []setupModel{{"opus", t1, nil}, {"sonnet", t23, nil}}},
	{model.RunnerCursor, 16, []setupModel{{"auto", t123, nil}}},
	{model.RunnerKimi, 8, []setupModel{{"kimi-code/k3.1-pro", t123, nil}}},
	{model.RunnerAGY, 6, []setupModel{{"gemini-4-argon-high", t12, nil}}},
}

const setupUsage = `setup — find installed CLIs, check sign-in, and prepare channels and positioning
purpose: a first-run helper. Without --yes it only prints the plan.

parameters:
  --yes    create the planned channels and propose the recommended positioning
  --json
`

func cmdSetup(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		fmt.Fprint(stdout, setupUsage)
		return 0
	}
	fs := newFlagSet("setup", "find installed CLIs, check sign-in, and prepare channels and positioning", "--yes --json", stdout, stderr)
	yes := fs.Bool("yes", false, "apply the plan")
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	resolved, err := resolveHome(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	open := openStore
	if *yes {
		open = openStoreForWrite
	}
	db, err := open(resolved)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	ctx := context.Background()
	existing, err := db.ListChannels(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	have := map[model.RunnerName]string{}
	for _, ch := range existing {
		have[ch.Runner] = ch.ID
	}
	type row struct {
		Runner string `json:"runner"`
		Status string `json:"status"` // ready, configured, not_signed_in, not_installed
		Next   string `json:"next,omitempty"`
		Models string `json:"models,omitempty"`
	}
	var rows []row
	var ready []setupPlan
	for _, p := range setupPlans {
		bin := runnerCLI[p.runner].bin
		names := modelNames(p.models)
		r := row{Runner: string(p.runner), Models: strings.Join(names, ", ")}
		switch {
		case have[p.runner] != "":
			r.Status, r.Next = "configured", "channel "+have[p.runner]+" already uses it"
		case !onPath(bin):
			r.Status = "not_installed"
		case loginProbe(ctx, p.runner, bin, true) == loginMissing:
			r.Status, r.Next = "not_signed_in", "run "+model.LoginCommand(p.runner)+", then rerun cardex setup"
		default:
			r.Status = "ready"
			ready = append(ready, p)
		}
		rows = append(rows, r)
	}

	var created []string
	var proposal string
	if *yes {
		for _, p := range ready {
			ch := model.Channel{ID: string(p.runner), Runner: p.runner, Health: model.HealthOK,
				Models: modelNames(p.models), Concurrency: p.concurrency}
			if _, err := db.GetChannel(ctx, ch.ID); err == nil {
				ch.ID = ch.ID + "-setup"
			}
			if err := db.CreateChannel(ctx, &ch); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			created = append(created, ch.ID)
		}
		proposal, err = proposeSetup(ctx, resolved, ready)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	if asJSON {
		return writeJSON(stdout, map[string]any{"clis": rows, "created": created, "proposal": proposal})
	}
	for _, r := range rows {
		line := fmt.Sprintf("%-9s %-14s", r.Runner, r.Status)
		if r.Status == "ready" {
			line += " models: " + r.Models
		}
		if r.Next != "" {
			line += " (" + r.Next + ")"
		}
		fmt.Fprintln(stdout, strings.TrimRight(line, " "))
	}
	switch {
	case !*yes && len(ready) > 0:
		fmt.Fprintln(stdout, "\nnext: cardex setup --yes   (adds a channel per ready CLI and proposes a positioning table for you to approve)")
	case !*yes && len(existing) == 0:
		fmt.Fprintln(stdout, "\nno ready CLI: install one and sign in, then rerun cardex setup")
	case *yes:
		if len(created) > 0 {
			fmt.Fprintln(stdout, "\ncreated channels:", strings.Join(created, ", "))
		}
		if proposal != "" {
			fmt.Fprintln(stdout, "proposed positioning", proposal, "— review with cardex positioning proposals, then approve:")
			fmt.Fprintln(stdout, "  cardex positioning approve", proposal)
		}
		fmt.Fprintln(stdout, "then: cardex project add -id <id> -repo <path>   and start cardex serve")
	}
	return 0
}

func modelNames(ms []setupModel) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.name)
	}
	return out
}

func onPath(bin string) bool {
	_, err := proc.LookPath(bin, nil)
	return err == nil
}

// proposeSetup proposes the recommended tiers when nothing is approved or
// pending. An approved table is left alone: setup never overrides it.
func proposeSetup(ctx context.Context, home string, plans []setupPlan) (string, error) {
	if len(plans) == 0 {
		return "", nil
	}
	h, err := positioning.Open(home)
	if err != nil {
		return "", err
	}
	defer h.Close()
	if ok, err := h.Approved(ctx); err != nil || ok {
		return "", err
	}
	if pending, err := h.Pending(); err != nil || len(pending) > 0 {
		if len(pending) > 0 {
			return pending[len(pending)-1].ID, nil
		}
		return "", err
	}
	var entries []model.Positioning
	for i, p := range plans {
		for _, m := range p.models {
			caps := m.caps
			if caps == nil {
				caps = []string{}
			}
			entries = append(entries, model.Positioning{
				ID:           "setup-" + slugID(m.name),
				Model:        m.name,
				Family:       positioning.FamilyOf(m.name, string(p.runner)),
				Tiers:        m.tiers,
				Capabilities: caps,
				Preference:   i + 1,
			})
		}
	}
	prop, err := h.Propose(ctx, entries)
	if err != nil {
		return "", err
	}
	return prop.ID, nil
}

func slugID(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}
