package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
)

const objectiveResumeLimit = 3

type objectiveResumeView struct {
	ID               string                  `json:"id"`
	Title            string                  `json:"title"`
	Project          string                  `json:"project"`
	Status           model.ObjectiveStatus   `json:"status"`
	Phase            string                  `json:"phase"`
	Scope            []objectiveResumeNode   `json:"approved_scope"`
	ScopeTotal       int                     `json:"approved_scope_total"`
	Pending          []objectiveResumeAnswer `json:"pending_approvals"`
	PendingTotal     int                     `json:"pending_approvals_total"`
	Recent           []objectiveResumeAnswer `json:"recent_decisions"`
	DecisionTotal    int                     `json:"decisions_total"`
	UndatedDecisions int                     `json:"undated_decisions"`
}

type objectiveResumeNode struct {
	ID    string      `json:"id"`
	Layer model.Layer `json:"layer"`
	Doc   string      `json:"doc,omitempty"`
}

type objectiveResumeAnswer struct {
	ID     string             `json:"id"`
	Scope  string             `json:"scope,omitempty"`
	Kind   model.QuestionKind `json:"kind"`
	Text   string             `json:"text"`
	Answer string             `json:"answer,omitempty"`
}

func objectiveResumeBrief(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("objective brief", "recover the current objective in one screen", "<id> --json", stdout, stderr)
	if code, ok := parseSet(fs, hoistFlags(fs, args)); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "objective brief <id>")
		return 2
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	var view objectiveResumeView
	err = db.WithTx(context.Background(), func(ctx context.Context) error {
		o, err := db.GetObjective(ctx, fs.Arg(0))
		if err != nil {
			return err
		}
		nodes, err := db.ListNodes(ctx, o.ID)
		if err != nil {
			return err
		}
		cards, err := db.ListCardWaitStates(ctx, o.ID)
		if err != nil {
			return err
		}
		qs, err := db.ListQuestions(ctx, o.ID)
		if err != nil {
			return err
		}
		events, err := db.ListEvents(ctx, "objective:"+o.ID)
		if err != nil {
			return err
		}
		view = buildObjectiveResume(o, nodes, cards, qs, events)
		return nil
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if asJSON {
		return writeJSON(stdout, view)
	}
	fmt.Fprintf(stdout, "%s\t%s\nproject\t%s\nphase\t%s\t(status: %s)\n", view.ID, view.Title, view.Project, view.Phase, view.Status)
	fmt.Fprintf(stdout, "approved scope (frozen)\t%d\n", view.ScopeTotal)
	for _, n := range view.Scope {
		fmt.Fprintf(stdout, "  %s\t%s\t%s\n", n.ID, n.Layer, n.Doc)
	}
	fmt.Fprintf(stdout, "pending approvals\t%d\n", view.PendingTotal)
	for _, q := range view.Pending {
		fmt.Fprintf(stdout, "  %s\t%s\t%s\t%s\n", q.ID, q.Kind, q.Scope, q.Text)
	}
	fmt.Fprintf(stdout, "recent decisions\t%d dated / %d total\n", view.DecisionTotal-view.UndatedDecisions, view.DecisionTotal)
	for _, q := range view.Recent {
		fmt.Fprintf(stdout, "  %s\t%s → %s\n", q.ID, q.Text, q.Answer)
	}
	return 0
}

func buildObjectiveResume(o model.Objective, nodes []model.DesignNode, cards []model.Card, qs []model.Question, events []model.Event) objectiveResumeView {
	v := objectiveResumeView{
		ID: clipBriefText(o.ID), Title: clipBriefText(o.Title), Project: clipBriefText(o.Project), Status: o.Status,
		Phase: string(o.Status), Scope: []objectiveResumeNode{}, Pending: []objectiveResumeAnswer{}, Recent: []objectiveResumeAnswer{},
	}
	frozen := 0
	for _, n := range nodes {
		if n.Status != model.NodeFrozen {
			continue
		}
		frozen++
		if len(v.Scope) < objectiveResumeLimit {
			v.Scope = append(v.Scope, objectiveResumeNode{clipBriefText(n.ID), n.Layer, clipBriefText(n.DocPath)})
		}
	}
	v.ScopeTotal = frozen
	answered := make(map[string]objectiveResumeAnswer)
	for _, q := range qs {
		row := objectiveResumeAnswer{clipBriefText(q.ID), clipBriefText(q.Scope), q.Kind, clipBriefText(q.Text), clipBriefText(q.Answer)}
		if strings.TrimSpace(q.Answer) == "" {
			v.PendingTotal++
			if len(v.Pending) < objectiveResumeLimit {
				v.Pending = append(v.Pending, row)
			}
		} else {
			answered[q.ID] = row
		}
	}
	v.DecisionTotal = len(answered)
	decided := make(map[string]bool)
	slices.SortFunc(events, func(a, b model.Event) int {
		if eventNewer(a, b) {
			return -1
		}
		if eventNewer(b, a) {
			return 1
		}
		return 0
	})
	for _, e := range events {
		if e.Type != model.EventObjectiveDecision {
			continue
		}
		var body struct {
			Question string `json:"question"`
		}
		if json.Unmarshal(e.Data, &body) != nil || decided[body.Question] {
			continue
		}
		row, ok := answered[body.Question]
		if !ok {
			continue
		}
		decided[body.Question] = true
		if len(v.Recent) < objectiveResumeLimit {
			v.Recent = append(v.Recent, row)
		}
	}
	v.UndatedDecisions = len(answered) - len(decided)
	if o.Status == model.ObjectiveActive {
		switch {
		case v.PendingTotal > 0:
			v.Phase = "awaiting_approval"
		case hasObjectiveWork(cards):
			v.Phase = "execution"
		case o.Mode == model.ModeManual:
			v.Phase = "manual"
		case len(nodes) == 0 || frozen < len(nodes):
			v.Phase = "design"
		default:
			v.Phase = "planning"
		}
	}
	return v
}

func hasObjectiveWork(cards []model.Card) bool {
	for _, c := range cards {
		switch c.Status {
		case model.CardReady, model.CardWaiting, model.CardRunning, model.CardHarvesting, model.CardReviewing, model.CardMergeQueued, model.CardNeedsInput, model.CardFailed:
			return true
		}
	}
	return false
}
