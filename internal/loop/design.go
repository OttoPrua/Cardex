package loop

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/notify"
	"github.com/OttoPrua/Cardex/internal/question"
	"github.com/OttoPrua/Cardex/internal/store"
	"github.com/OttoPrua/Cardex/internal/workspace"
)

// Deps is the design-phase edge: store, workspace, and notify.
// designStep keeps parsing and freeze decisions off this struct.
type Deps struct {
	Store  *store.Store
	WS     workspace.Manager
	Notify notify.Deliverer
}

type engine struct{ Deps }

// New returns an Engine whose Step runs only the design phase.
// DevStep stays unimplemented and Step does not call it.
func New(st *store.Store, ws workspace.Manager, n notify.Deliverer) Engine {
	return &engine{Deps: Deps{Store: st, WS: ws, Notify: n}}
}

func (e *engine) Step(ctx context.Context, objectiveID string) error {
	return designStep(ctx, e.Deps, objectiveID)
}

func designStep(ctx context.Context, d Deps, objectiveID string) error {
	if d.Store == nil {
		return errors.New("loop: designStep: nil store")
	}
	if strings.TrimSpace(objectiveID) == "" {
		return errors.New("loop: objective id required")
	}
	obj, err := d.Store.GetObjective(ctx, objectiveID)
	if err != nil {
		return err
	}
	if obj.Mode != model.ModeAuto {
		return nil
	}
	researched, err := d.absorbTerminal(ctx, obj)
	if err != nil {
		return err
	}
	if err := d.settleFrozenResearch(ctx, obj); err != nil {
		return err
	}
	if err := d.dispatchResearch(ctx, obj); err != nil {
		return err
	}
	if err := d.advanceNodes(ctx, obj); err != nil {
		return err
	}
	if err := d.progressNodes(ctx, obj, researched); err != nil {
		return err
	}
	return d.ensureDirection(ctx, obj)
}

func (d Deps) questions() question.Service {
	return question.New(d.Store, d.Notify)
}

func (d Deps) absorbTerminal(ctx context.Context, obj model.Objective) (map[string]bool, error) {
	researched := map[string]bool{}
	cards, err := d.Store.ListCards(ctx, obj.ID)
	if err != nil {
		return nil, err
	}
	for i := range cards {
		if !neverMerge(cards[i].Kind) {
			continue
		}
		if cards[i].Status != model.CardMergeQueued && cards[i].Status != model.CardMerged {
			continue
		}
		cards[i].Status = model.CardDone
		if err := d.Store.UpdateCard(ctx, &cards[i]); err != nil {
			return nil, err
		}
	}
	cards, err = d.Store.ListCards(ctx, obj.ID)
	if err != nil {
		return nil, err
	}
	for _, card := range cards {
		if !terminalCard(card.Status) {
			continue
		}
		consumed, err := d.currentAttemptConsumed(ctx, card)
		if err != nil {
			return nil, err
		}
		if consumed {
			if err := d.backfillSkippedResearch(ctx, obj, card); err != nil {
				return nil, err
			}
			continue
		}
		if err := d.absorbOne(ctx, obj, card, researched); err != nil {
			return nil, err
		}
	}
	return researched, nil
}

func neverMerge(k model.CardKind) bool {
	return k == model.KindResearch || k == model.KindSpike
}

func (d Deps) absorbOne(ctx context.Context, obj model.Objective, card model.Card, researched map[string]bool) error {
	text, base, outcome, attemptID, err := d.harvested(ctx, card)
	if err != nil {
		return err
	}
	if card.Kind == model.KindResearch && card.Node != "" && !skippedOutcome(outcome, card.Status) {
		handled, err := d.attemptHandled(ctx, attemptID)
		if err != nil {
			return err
		}
		if err := d.absorbAttempt(ctx, obj, card, attemptID, base, text, outcome); err != nil {
			return err
		}
		if !handled {
			researched[card.Node] = true
		}
		return nil
	}
	return d.absorbAttempt(ctx, obj, card, attemptID, base, text, outcome)
}

func (d Deps) absorbInventory(ctx context.Context, obj model.Objective, card model.Card, result, base string) error {
	q := model.OpenQuestion{
		ID: "Q0", Type: model.OQFact, Text: "盘点 " + obj.Title,
		Status: model.OQUnanswered, CitedPaths: []string{}, Commit: base,
	}
	secs := splitSections(result)
	if b, ok := parseFactSection(secs["事实"])["Q0"]; ok {
		applyBlock(&q, b, base)
	} else {
		text := strings.TrimSpace(secs["事实"])
		if text == "" {
			text = strings.TrimSpace(secs["结论"])
		}
		if text != "" {
			applyBlock(&q, factBlock{present: true, answer: text, evidence: text}, base)
		}
	}
	if q.Text == "" {
		q.Text = "盘点 " + obj.Title
	}
	raw, err := json.Marshal(q)
	if err != nil {
		return err
	}
	// A card-level absorb is normally written once. A later passed attempt
	// still has to replace an earlier empty Q0, so an answered record is appended.
	seen, err := d.hasEvent(ctx, "card:"+card.ID, evAbsorbed)
	if err != nil {
		return err
	}
	if !seen {
		return d.markAbsorbed(ctx, card.ID, raw)
	}
	if q.Status != model.OQAnswered {
		return nil
	}
	return d.Store.AppendEvent(ctx, &model.Event{Entity: "card:" + card.ID, Type: evAbsorbed, Data: raw})
}

func (d Deps) applyDesign(ctx context.Context, obj model.Objective, card model.Card, result string) error {
	if card.Node == "" {
		return nil
	}
	node, err := d.Store.GetNode(ctx, card.Node)
	if err != nil {
		return err
	}
	secs := splitSections(result)
	var fresh []model.OpenQuestion
	for _, line := range parseQuestionLines(secs["开放问题"]) {
		switch line.kind {
		case "drop":
			dropOpenQuestion(&node, line.id)
		case "recheck":
			applyRecheck(&node, line.id)
		case "add":
			q, added := addOpenQuestion(&node, line.text, line.qtype)
			if added {
				fresh = append(fresh, q)
			}
		}
	}
	if err := d.Store.UpdateNode(ctx, &node); err != nil {
		return err
	}
	for _, q := range fresh {
		if q.Type == model.OQExperiment {
			if err := d.createSpike(ctx, obj, node, q); err != nil {
				return err
			}
		}
	}
	for _, ch := range parseChildLines(secs["子节点"]) {
		if err := d.createChild(ctx, obj, node, ch); err != nil {
			return err
		}
	}
	if judgeResultFreeze(result) == freezeYes {
		if err := d.noteFreezable(ctx, node.ID, card.ID, node.Status == model.NodeReopened); err != nil {
			return err
		}
	}
	return nil
}

func (d Deps) absorbSpike(ctx context.Context, obj model.Objective, card model.Card, result string) error {
	if card.Node != "" {
		id := spikeQuestionID(result)
		if id == "" {
			id = spikeQuestionID(card.Body)
		}
		if id != "" {
			conclusion := strings.TrimSpace(splitSections(result)["结论"])
			if conclusion != "" {
				node, err := d.Store.GetNode(ctx, card.Node)
				if err != nil {
					return err
				}
				for i := range node.OpenQuestions {
					if node.OpenQuestions[i].ID != id {
						continue
					}
					node.OpenQuestions[i].Answer = conclusion
					node.OpenQuestions[i].Status = model.OQAnswered
				}
				if err := d.Store.UpdateNode(ctx, &node); err != nil {
					return err
				}
			}
		}
	}
	if card.Status != model.CardDone {
		card.Status = model.CardDone
		if err := d.Store.UpdateCard(ctx, &card); err != nil {
			return err
		}
	}
	return d.markAbsorbed(ctx, card.ID, nil)
}

func spikeQuestionID(body string) string {
	for _, id := range questionIDsInBody(body) {
		return id
	}
	return ""
}

func (d Deps) absorbDevReview(ctx context.Context, obj model.Objective, card model.Card, result string, outcome model.Outcome) error {
	secs := splitSections(result)
	problem := strings.TrimSpace(secs["设计问题"])
	if problem != "" && card.Node != "" {
		if _, err := d.questions().Create(ctx, model.Question{
			Objective: obj.ID,
			Scope:     card.Node,
			ScopeKind: "node",
			Kind:      model.QuestionReopen,
			Text:      problem,
			Options:   []string{"同意", "拒绝"},
		}); err != nil {
			return err
		}
	}
	if card.Kind == model.KindReview && problem == "" && passedReview(secs["复核结论"]) && card.Node != "" && freezePassAllowed(card.Status, outcome) {
		raw, err := json.Marshal(reviewBind{Design: card.ID})
		if err != nil {
			return err
		}
		if err := d.Store.AppendEvent(ctx, &model.Event{Entity: "card:" + card.ID, Type: evReviewPass, Data: raw}); err != nil {
			return err
		}
	}
	return d.markAbsorbed(ctx, card.ID, nil)
}

func (d Deps) createSpike(ctx context.Context, obj model.Objective, node model.DesignNode, q model.OpenQuestion) error {
	if obj.Mode != model.ModeAuto {
		return nil
	}
	cards, err := d.Store.ListCards(ctx, obj.ID)
	if err != nil {
		return err
	}
	needle := q.ID + ":"
	for _, c := range cards {
		if c.Kind == model.KindSpike && c.Node == node.ID && strings.Contains(c.Body, needle) {
			return nil
		}
	}
	return d.addCard(ctx, obj, &model.Card{
		Objective: obj.ID,
		Node:      node.ID,
		Kind:      model.KindSpike,
		Title:     "原型 " + q.ID,
		Body:      q.ID + ": " + q.Text + "\n",
		Tier:      model.TierT2,
		Status:    model.CardReady,
	})
}

func (d Deps) createChild(ctx context.Context, obj model.Objective, parent model.DesignNode, spec childSpec) error {
	if obj.Mode != model.ModeAuto {
		return nil
	}
	nodes, err := d.Store.ListNodes(ctx, obj.ID)
	if err != nil {
		return err
	}
	cards, err := d.Store.ListCards(ctx, obj.ID)
	if err != nil {
		return err
	}
	title := "设计 " + string(spec.layer) + " " + spec.title
	for _, n := range nodes {
		if n.Parent != parent.ID || n.Layer != spec.layer {
			continue
		}
		for _, c := range cards {
			if c.Node == n.ID && c.Title == title {
				return nil
			}
		}
	}
	child := &model.DesignNode{
		Objective:     obj.ID,
		Parent:        parent.ID,
		Layer:         spec.layer,
		Status:        model.NodeDrafting,
		OpenQuestions: model.OpenQuestionList{},
	}
	if err := d.Store.CreateNode(ctx, child); err != nil {
		return err
	}
	if err := d.assignDocPath(ctx, obj, child); err != nil {
		return err
	}
	return d.createWorkCard(ctx, obj, *child, model.KindDesign, title)
}

func (d Deps) progressNodes(ctx context.Context, obj model.Objective, researched map[string]bool) error {
	nodes, err := d.Store.ListNodes(ctx, obj.ID)
	if err != nil {
		return err
	}
	cards, err := d.Store.ListCards(ctx, obj.ID)
	if err != nil {
		return err
	}
	questions, err := d.Store.ListQuestions(ctx, obj.ID)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		blocked, err := d.unmergedDesignFile(ctx, obj, node, cards)
		if err != nil {
			return err
		}
		if blocked {
			continue
		}
		if followKind(node.Status) == model.KindPlan {
			if unfinishedResearch(cards, node.ID) || hasKind(cards, node.ID, model.KindPlan) {
				continue
			}
			if err := d.createWorkCard(ctx, obj, node, model.KindPlan, workTitle(model.KindPlan)); err != nil {
				return err
			}
			cards, err = d.Store.ListCards(ctx, obj.ID)
			if err != nil {
				return err
			}
			continue
		}
		if !mayReceiveNext(node, cards, questions, researched[node.ID]) {
			continue
		}
		kind := followKind(node.Status)
		if err := d.createWorkCard(ctx, obj, node, kind, workTitle(kind)); err != nil {
			return err
		}
		cards, err = d.Store.ListCards(ctx, obj.ID)
		if err != nil {
			return err
		}
	}
	return nil
}

func mayReceiveNext(node model.DesignNode, cards []model.Card, questions []model.Question, researched bool) bool {
	if node.ID == "" || unfinishedResearch(cards, node.ID) || activeWork(cards, node.ID) || waitingHuman(questions, node.ID) {
		return false
	}
	if researched {
		return true
	}
	switch node.Status {
	case model.NodeDrafting, model.NodeReopened:
		return true
	default:
		return false
	}
}

func waitingHuman(questions []model.Question, nodeID string) bool {
	for _, q := range questions {
		if q.Scope == nodeID && strings.TrimSpace(q.Answer) == "" {
			return true
		}
	}
	return false
}

func workTitle(kind model.CardKind) string {
	switch kind {
	case model.KindPlan:
		return "拆卡"
	case model.KindReplan:
		return "重规划"
	default:
		return "设计"
	}
}

func followKind(st model.NodeStatus) model.CardKind {
	switch st {
	case model.NodeFrozen:
		return model.KindPlan
	case model.NodeReopened:
		return model.KindReplan
	default:
		return model.KindDesign
	}
}

func (d Deps) advanceNodes(ctx context.Context, obj model.Objective) error {
	nodes, err := d.Store.ListNodes(ctx, obj.ID)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		if err := d.advanceNode(ctx, obj, node.ID); err != nil {
			return err
		}
	}
	return nil
}

func (d Deps) advanceNode(ctx context.Context, obj model.Objective, nodeID string) error {
	node, err := d.Store.GetNode(ctx, nodeID)
	if err != nil {
		return err
	}
	questions, err := d.Store.ListQuestions(ctx, obj.ID)
	if err != nil {
		return err
	}
	cards, err := d.Store.ListCards(ctx, obj.ID)
	if err != nil {
		return err
	}
	if err := d.applyReopen(ctx, obj, &node, questions, cards); err != nil {
		return err
	}
	node, err = d.Store.GetNode(ctx, nodeID)
	if err != nil {
		return err
	}
	if node.Status == model.NodeReopened {
		if err := d.promoteReopened(ctx, &node); err != nil {
			return err
		}
	}
	if node.Status == model.NodeFrozen {
		return nil
	}
	proposed, err := d.hasEvent(ctx, "node:"+node.ID, evFreezable)
	if err != nil {
		return err
	}
	if proposed && !nodeBlocked(node) && node.Status == model.NodeDrafting {
		node.Status = model.NodeFreezable
		if err := d.Store.UpdateNode(ctx, &node); err != nil {
			return err
		}
	}
	if node.Status != model.NodeFreezable {
		return nil
	}
	questions, err = d.Store.ListQuestions(ctx, obj.ID)
	if err != nil {
		return err
	}
	cards, err = d.Store.ListCards(ctx, obj.ID)
	if err != nil {
		return err
	}
	notes, err := d.reopenNotes(ctx, node.ID)
	if err != nil {
		return err
	}
	blocked := nodeBlocked(node)
	switch decideFreeze(node.Layer, obj.FreezeMode) {
	case freezeNow:
		if blocked {
			return nil
		}
		return d.freezeNode(ctx, obj, node)
	case freezeAsk:
		stored, ok := currentFreeze(questions, node.ID, spentFreeze(notes))
		var q model.Question
		var err error
		switch {
		case ok && strings.TrimSpace(stored.Answer) == "":
			// Create reuses this unanswered row and retries a delivery that failed earlier.
			q, err = d.questions().Create(ctx, stored)
		case ok:
			q = stored
		default:
			q, err = d.questions().Create(ctx, model.Question{
				Objective: obj.ID,
				Scope:     node.ID,
				ScopeKind: "node",
				Kind:      model.QuestionFreeze,
				Text:      "冻结 " + string(node.Layer) + " " + node.ID,
				Options:   []string{"同意", "拒绝"},
			})
		}
		if err != nil {
			return err
		}
		if blocked || !consented(q) {
			return nil
		}
		return d.freezeNode(ctx, obj, node)
	case freezeReview:
		designID, err := d.latestFreezableCard(ctx, node.ID)
		if err != nil {
			return err
		}
		review, ok, err := d.boundReview(ctx, cards, node.ID, designID)
		if err != nil {
			return err
		}
		if !ok {
			family, err := d.designFamily(ctx, designID)
			if err != nil {
				return err
			}
			created := &model.Card{
				Objective:    obj.ID,
				Node:         node.ID,
				Kind:         model.KindReview,
				Title:        "复核 " + node.ID,
				Body:         "复核节点 " + node.ID + "\n",
				Tier:         model.TierT3,
				Status:       model.CardReady,
				AuthorFamily: family,
				Deps:         []string{designID},
			}
			if err := d.addCard(ctx, obj, created); err != nil {
				return err
			}
			raw, err := json.Marshal(reviewBind{Design: designID})
			if err != nil {
				return err
			}
			return d.Store.AppendEvent(ctx, &model.Event{Entity: "card:" + created.ID, Type: evReviewBind, Data: raw})
		}
		passed, err := d.hasEvent(ctx, "card:"+review.ID, evReviewPass)
		if err != nil {
			return err
		}
		if blocked || !passed {
			return nil
		}
		return d.freezeNode(ctx, obj, node)
	default:
		return nil
	}
}

func (d Deps) applyReopen(ctx context.Context, obj model.Objective, node *model.DesignNode, questions []model.Question, cards []model.Card) error {
	notes, err := d.reopenNotes(ctx, node.ID)
	if err != nil {
		return err
	}
	q, ok := reopenUnused(notes, questions, node.ID)
	if !ok {
		return nil
	}
	replanID, inFlight := replanInFlight(cards, node.ID)
	node.Status = model.NodeReopened
	if err := d.Store.UpdateNode(ctx, node); err != nil {
		return err
	}
	for i := range cards {
		if cards[i].Node != node.ID || !notStartedCard(cards[i].Status) {
			continue
		}
		cards[i].Status = model.CardPaused
		if err := d.Store.UpdateCard(ctx, &cards[i]); err != nil {
			return err
		}
	}
	if !inFlight {
		if err := d.createWorkCard(ctx, obj, *node, model.KindReplan, "重规划"); err != nil {
			return err
		}
		updated, err := d.Store.ListCards(ctx, obj.ID)
		if err != nil {
			return err
		}
		for _, c := range updated {
			if c.Node == node.ID && c.Kind == model.KindReplan && !terminalCard(c.Status) && c.Status != model.CardPaused {
				replanID = c.ID
			}
		}
	}
	var freezeIDs []string
	for _, item := range questions {
		if item.Kind == model.QuestionFreeze && item.Scope == node.ID {
			freezeIDs = append(freezeIDs, item.ID)
		}
	}
	raw, err := json.Marshal(reopenNote{Question: q.ID, Freeze: freezeIDs, Replan: replanID})
	if err != nil {
		return err
	}
	return d.Store.AppendEvent(ctx, &model.Event{Entity: "node:" + node.ID, Type: evReopenUsed, Data: raw})
}

func (d Deps) freezeNode(ctx context.Context, obj model.Objective, node model.DesignNode) error {
	if node.Status != model.NodeFrozen {
		node.Status = model.NodeFrozen
		if err := d.Store.UpdateNode(ctx, &node); err != nil {
			return err
		}
	}
	cards, err := d.Store.ListCards(ctx, obj.ID)
	if err != nil {
		return err
	}
	if hasKind(cards, node.ID, model.KindPlan) {
		return nil
	}
	blocked, err := d.unmergedDesignFile(ctx, obj, node, cards)
	if err != nil || blocked {
		return err
	}
	return d.createWorkCard(ctx, obj, node, model.KindPlan, "拆卡")
}

func currentFreeze(questions []model.Question, nodeID string, spent map[string]bool) (model.Question, bool) {
	for _, q := range questions {
		if q.Kind == model.QuestionFreeze && q.Scope == nodeID && !spent[q.ID] {
			return q, true
		}
	}
	return model.Question{}, false
}

func (d Deps) ensureDirection(ctx context.Context, obj model.Objective) error {
	if obj.Mode != model.ModeAuto {
		return nil
	}
	nodes, err := d.Store.ListNodes(ctx, obj.ID)
	if err != nil {
		return err
	}
	if len(nodes) > 0 {
		return nil
	}
	cards, err := d.Store.ListCards(ctx, obj.ID)
	if err != nil {
		return err
	}
	inv := inventoryCard(cards)
	if inv == nil {
		return d.createInventory(ctx, obj)
	}
	if !terminalCard(inv.Status) {
		return nil
	}
	q0, ok, err := d.inventoryQ0(ctx, inv.ID)
	if err != nil || !ok {
		return err
	}
	node := &model.DesignNode{
		Objective:     obj.ID,
		Layer:         model.LayerDirection,
		Status:        model.NodeDrafting,
		OpenQuestions: model.OpenQuestionList{q0},
	}
	if err := d.Store.CreateNode(ctx, node); err != nil {
		return err
	}
	if err := d.assignDocPath(ctx, obj, node); err != nil {
		return err
	}
	return d.createWorkCard(ctx, obj, *node, model.KindDesign, "设计方向")
}

func inventoryCard(cards []model.Card) *model.Card {
	for i := range cards {
		c := &cards[i]
		if c.Kind == model.KindResearch && c.Node == "" && (strings.Contains(c.Title, "盘点") || strings.Contains(c.Body, "Q0")) {
			return c
		}
	}
	return nil
}

func (d Deps) createInventory(ctx context.Context, obj model.Objective) error {
	if obj.Mode != model.ModeAuto {
		return nil
	}
	body := "Q0\n盘点仓库结构、与目标相关的模块、现有文档、测试和构建命令。\n目标：" + obj.Title + "\n"
	return d.addCard(ctx, obj, &model.Card{
		Objective: obj.ID,
		Kind:      model.KindResearch,
		Title:     "盘点",
		Body:      body,
		Tier:      model.TierT1,
		Status:    model.CardReady,
	})
}

func (d Deps) inventoryQ0(ctx context.Context, cardID string) (model.OpenQuestion, bool, error) {
	ev, err := d.Store.ListEvents(ctx, "card:"+cardID)
	if err != nil {
		return model.OpenQuestion{}, false, err
	}
	var found model.OpenQuestion
	ok := false
	for _, e := range ev {
		if e.Type != evAbsorbed || len(e.Data) == 0 {
			continue
		}
		var q model.OpenQuestion
		if err := json.Unmarshal(e.Data, &q); err != nil {
			return model.OpenQuestion{}, false, err
		}
		if q.ID != "Q0" {
			continue
		}
		if q.CitedPaths == nil {
			q.CitedPaths = []string{}
		}
		found = q
		ok = true
	}
	return found, ok, nil
}

func (d Deps) createWorkCard(ctx context.Context, obj model.Objective, node model.DesignNode, kind model.CardKind, title string) error {
	if obj.Mode != model.ModeAuto {
		return nil
	}
	cards, err := d.Store.ListCards(ctx, obj.ID)
	if err != nil {
		return err
	}
	if unfinishedResearch(cards, node.ID) {
		return nil
	}
	fresh, err := d.Store.GetNode(ctx, node.ID)
	if err != nil {
		return err
	}
	if err := d.markStale(ctx, obj, &fresh); err != nil {
		return err
	}
	if err := d.Store.UpdateNode(ctx, &fresh); err != nil {
		return err
	}
	facts, err := d.factList(ctx, obj, fresh)
	if err != nil {
		return err
	}
	body, err := summaryBody(title, facts)
	if err != nil {
		return err
	}
	if err := d.addCard(ctx, obj, &model.Card{
		Objective: obj.ID,
		Node:      node.ID,
		Kind:      kind,
		Title:     title,
		Body:      body,
		Tier:      model.DefaultTier(kind),
		Status:    model.CardReady,
	}); err != nil {
		return err
	}
	if kind != model.KindPlan {
		return nil
	}
	return d.consumeSettledResearch(ctx, obj.ID, node.ID)
}

// consumeSettledResearch ties already-settled research to the plan just created.
// Later research is left pending so it can receive its own successor.
func (d Deps) consumeSettledResearch(ctx context.Context, objective, nodeID string) error {
	cards, err := d.Store.ListCards(ctx, objective)
	if err != nil {
		return err
	}
	var research []model.Card
	for _, c := range cards {
		if c.Node == nodeID && c.Kind == model.KindResearch && terminalCard(c.Status) {
			research = append(research, c)
		}
	}
	settled, err := d.researchSettled(ctx, research)
	if err != nil || !settled {
		return err
	}
	for _, c := range research {
		if err := d.markContinued(ctx, c.ID); err != nil {
			return err
		}
	}
	return nil
}

func (d Deps) addCard(ctx context.Context, obj model.Objective, c *model.Card) error {
	if obj.Mode != model.ModeAuto && manualKind(c.Kind) {
		return nil
	}
	if c.Status == "" {
		c.Status = model.CardReady
	}
	if c.Tier == "" || c.Tier == model.TierNone {
		c.Tier = model.DefaultTier(c.Kind)
	}
	return d.Store.CreateCard(ctx, c)
}

func (d Deps) rootDirection(ctx context.Context, objectiveID string) (model.DesignNode, bool, error) {
	nodes, err := d.Store.ListNodes(ctx, objectiveID)
	if err != nil {
		return model.DesignNode{}, false, err
	}
	for _, n := range nodes {
		if n.Layer == model.LayerDirection && n.Parent == "" {
			return n, true, nil
		}
	}
	return model.DesignNode{}, false, nil
}

func unfinishedResearch(cards []model.Card, nodeID string) bool {
	for _, c := range cards {
		if c.Node == nodeID && c.Kind == model.KindResearch && !terminalCard(c.Status) {
			return true
		}
	}
	return false
}

func activeWork(cards []model.Card, nodeID string) bool {
	for _, c := range cards {
		if c.Node != nodeID || terminalCard(c.Status) || c.Status == model.CardPaused {
			continue
		}
		switch c.Kind {
		case model.KindDesign, model.KindPlan, model.KindReplan:
			return true
		}
	}
	return false
}

func hasKind(cards []model.Card, nodeID string, kind model.CardKind) bool {
	for _, c := range cards {
		if c.Node == nodeID && c.Kind == kind {
			return true
		}
	}
	return false
}

func (d Deps) hasEvent(ctx context.Context, entity, typ string) (bool, error) {
	ev, err := d.Store.ListEvents(ctx, entity)
	if err != nil {
		return false, err
	}
	for _, e := range ev {
		if e.Type == typ {
			return true, nil
		}
	}
	return false, nil
}

func (d Deps) noteEvent(ctx context.Context, entity, typ string, data []byte) error {
	ok, err := d.hasEvent(ctx, entity, typ)
	if err != nil {
		return err
	}
	if ok && typ != evResearch {
		return nil
	}
	if len(data) == 0 {
		data = []byte(`{}`)
	}
	return d.Store.AppendEvent(ctx, &model.Event{Entity: entity, Type: typ, Data: data})
}

func (d Deps) markAbsorbed(ctx context.Context, cardID string, data []byte) error {
	return d.noteEvent(ctx, "card:"+cardID, evAbsorbed, data)
}

type freezableNote struct {
	Card string `json:"card"`
	Gen  int    `json:"gen"`
}

type reviewBind struct {
	Design string `json:"design"`
}

type reopenNote struct {
	Question string   `json:"question"`
	Freeze   []string `json:"freeze"`
	Replan   string   `json:"replan"`
}

func (d Deps) noteFreezable(ctx context.Context, nodeID, cardID string, reopened bool) error {
	gen := 0
	if reopened {
		notes, err := d.reopenNotes(ctx, nodeID)
		if err != nil {
			return err
		}
		gen = len(notes)
	}
	raw, err := json.Marshal(freezableNote{Card: cardID, Gen: gen})
	if err != nil {
		return err
	}
	return d.Store.AppendEvent(ctx, &model.Event{Entity: "node:" + nodeID, Type: evFreezable, Data: raw})
}

func (d Deps) reopenNotes(ctx context.Context, nodeID string) ([]reopenNote, error) {
	ev, err := d.Store.ListEvents(ctx, "node:"+nodeID)
	if err != nil {
		return nil, err
	}
	var notes []reopenNote
	for _, e := range ev {
		if e.Type != evReopenUsed || len(e.Data) == 0 {
			continue
		}
		var n reopenNote
		if err := json.Unmarshal(e.Data, &n); err != nil {
			return nil, err
		}
		notes = append(notes, n)
	}
	return notes, nil
}

func reopenUnused(notes []reopenNote, questions []model.Question, nodeID string) (model.Question, bool) {
	used := map[string]bool{}
	for _, n := range notes {
		if n.Question != "" {
			used[n.Question] = true
		}
	}
	for _, q := range questions {
		if q.Kind == model.QuestionReopen && q.Scope == nodeID && consented(q) && !used[q.ID] {
			return q, true
		}
	}
	return model.Question{}, false
}

func spentFreeze(notes []reopenNote) map[string]bool {
	spent := map[string]bool{}
	for _, n := range notes {
		for _, id := range n.Freeze {
			if id != "" {
				spent[id] = true
			}
		}
	}
	return spent
}

func replanInFlight(cards []model.Card, nodeID string) (string, bool) {
	for _, c := range cards {
		if c.Node != nodeID || c.Kind != model.KindReplan {
			continue
		}
		if terminalCard(c.Status) || c.Status == model.CardPaused || notStartedCard(c.Status) {
			continue
		}
		return c.ID, true
	}
	return "", false
}

func (d Deps) promoteReopened(ctx context.Context, node *model.DesignNode) error {
	notes, err := d.reopenNotes(ctx, node.ID)
	if err != nil || len(notes) == 0 {
		return err
	}
	cardID, err := d.freezableForGen(ctx, node.ID, len(notes))
	if err != nil || cardID == "" {
		return err
	}
	card, err := d.Store.GetCard(ctx, cardID)
	if err != nil {
		return err
	}
	if card.Kind != model.KindReplan || card.Node != node.ID || !terminalCard(card.Status) || nodeBlocked(*node) {
		return nil
	}
	node.Status = model.NodeFreezable
	return d.Store.UpdateNode(ctx, node)
}

func (d Deps) freezableForGen(ctx context.Context, nodeID string, gen int) (string, error) {
	ev, err := d.Store.ListEvents(ctx, "node:"+nodeID)
	if err != nil {
		return "", err
	}
	var cardID string
	for _, e := range ev {
		if e.Type != evFreezable || len(e.Data) == 0 {
			continue
		}
		var n freezableNote
		if err := json.Unmarshal(e.Data, &n); err != nil {
			return "", err
		}
		if n.Gen == gen && n.Card != "" {
			cardID = n.Card
		}
	}
	return cardID, nil
}

func (d Deps) latestFreezableCard(ctx context.Context, nodeID string) (string, error) {
	ev, err := d.Store.ListEvents(ctx, "node:"+nodeID)
	if err != nil {
		return "", err
	}
	var cardID string
	for _, e := range ev {
		if e.Type != evFreezable || len(e.Data) == 0 {
			continue
		}
		var n freezableNote
		if json.Unmarshal(e.Data, &n) != nil || n.Card == "" {
			continue
		}
		cardID = n.Card
	}
	return cardID, nil
}

func (d Deps) boundReview(ctx context.Context, cards []model.Card, nodeID, designID string) (model.Card, bool, error) {
	for _, c := range cards {
		if c.Node != nodeID || c.Kind != model.KindReview {
			continue
		}
		ev, err := d.Store.ListEvents(ctx, "card:"+c.ID)
		if err != nil {
			return model.Card{}, false, err
		}
		for _, e := range ev {
			if e.Type != evReviewBind || len(e.Data) == 0 {
				continue
			}
			var b reviewBind
			if err := json.Unmarshal(e.Data, &b); err != nil {
				return model.Card{}, false, err
			}
			if b.Design == designID {
				return c, true, nil
			}
		}
	}
	return model.Card{}, false, nil
}

func (d Deps) designFamily(ctx context.Context, cardID string) (string, error) {
	if cardID == "" || d.Store == nil {
		return "", nil
	}
	card, err := d.Store.GetCard(ctx, cardID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(card.AuthorFamily) != "" {
		return card.AuthorFamily, nil
	}
	attempts, err := d.Store.ListAttempts(ctx, cardID)
	if err != nil {
		return "", err
	}
	var family string
	for _, a := range attempts {
		if f := attemptFamily(a); f != "" {
			family = f
		}
	}
	return family, nil
}

func attemptFamily(a model.Attempt) string {
	for _, c := range a.RouteReason.Candidates {
		if c.Family == "" {
			continue
		}
		if a.Route.Model == "" || c.Model == a.Route.Model {
			return c.Family
		}
	}
	for _, c := range a.RouteReason.Candidates {
		if c.Family != "" {
			return c.Family
		}
	}
	return ""
}

func (d Deps) assignDocPath(ctx context.Context, obj model.Objective, node *model.DesignNode) error {
	if node == nil || strings.TrimSpace(node.DocPath) != "" || node.ID == "" {
		return nil
	}
	dir := "docs/design"
	if obj.Project != "" {
		p, err := d.Store.GetProject(ctx, obj.Project)
		if err != nil {
			return err
		}
		if strings.TrimSpace(p.DesignDir) != "" {
			dir = strings.TrimSpace(p.DesignDir)
		}
	}
	dir = strings.Trim(strings.ReplaceAll(dir, "\\", "/"), "/")
	if dir == "" {
		dir = "docs/design"
	}
	node.DocPath = dir + "/" + obj.ID + "/" + node.ID + ".md"
	return d.Store.UpdateNode(ctx, node)
}

// AbsorbDisposed absorbs the attempt being disposed. The integrator sets
// sched.Runner.Dispose so that, before the worktree is removed, it calls
// AbsorbDisposed(ctx, Deps{Store, WS, Notify}, attemptID, res.Outcome, res.ResultText).
// resultText is the harvest payload. This does not read any attempt worktree,
// and it does not select an older attempt whose result file is still present.
func AbsorbDisposed(ctx context.Context, d Deps, attemptID string, outcome model.Outcome, resultText string) error {
	if d.Store == nil {
		return errors.New("loop: AbsorbDisposed: nil store")
	}
	attemptID = strings.TrimSpace(attemptID)
	if attemptID == "" {
		return errors.New("loop: AbsorbDisposed: attempt id required")
	}
	attempt, err := d.Store.GetAttempt(ctx, attemptID)
	if err != nil {
		return err
	}
	card, err := d.Store.GetCard(ctx, attempt.Card)
	if err != nil {
		return err
	}
	obj, err := d.Store.GetObjective(ctx, card.Objective)
	if err != nil {
		return err
	}
	return d.absorbAttempt(ctx, obj, card, attempt.ID, attempt.BaseCommit, resultText, outcome)
}

// designUnusable reports a missing or unparsable design, plan, or replan result.
// Empty text, a result_unparsable outcome, or text with no section heading
// is not a successful absorb.
func designUnusable(result string, outcome model.Outcome) bool {
	if outcome == model.OutcomeResultUnparsable {
		return true
	}
	text := strings.TrimSpace(result)
	if text == "" {
		return true
	}
	return !strings.Contains(text, "##")
}

// currentAttemptConsumed is true when this card's current attempt was already
// absorbed. A card-level loop.absorbed mark applies only when the card has no attempt.
func (d Deps) currentAttemptConsumed(ctx context.Context, card model.Card) (bool, error) {
	_, _, _, attemptID, err := d.harvested(ctx, card)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(attemptID) != "" {
		return d.attemptHandled(ctx, attemptID)
	}
	done, err := d.hasEvent(ctx, "card:"+card.ID, evAbsorbed)
	if err != nil || done {
		return done, err
	}
	return d.continuationSettled(ctx, card)
}

// recordSkippedResearch keeps the 未能核实 record for a research card that is
// already terminal and has no valid answer. It does not store the voided text.
// A skip while the card can still retry must not call this.
func (d Deps) recordSkippedResearch(ctx context.Context, obj model.Objective, card model.Card, base string) error {
	if card.Node == "" {
		return d.absorbInventory(ctx, obj, card, "", base)
	}
	node, err := d.Store.GetNode(ctx, card.Node)
	if err != nil {
		return err
	}
	ids, err := d.researchIDs(ctx, card)
	if err != nil {
		return err
	}
	changed := false
	for _, id := range ids {
		for i := range node.OpenQuestions {
			if node.OpenQuestions[i].ID != id || node.OpenQuestions[i].Status != model.OQResearching {
				continue
			}
			node.OpenQuestions[i].Status = model.OQUnanswered
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return d.Store.UpdateNode(ctx, &node)
}

// applyNormalize restores Deps[0] from this normalize attempt. A usable pass applies
// the normalize text and finishes the source the same way a passed design, plan,
// or replan absorb does. A failed, canceled, or unusable normalize sets the source
// to needs_input and does not apply the text.
func (d Deps) applyNormalize(ctx context.Context, obj model.Objective, card model.Card, text string, outcome model.Outcome) error {
	if len(card.Deps) == 0 || strings.TrimSpace(card.Deps[0]) == "" {
		return nil
	}
	source, err := d.Store.GetCard(ctx, card.Deps[0])
	if err != nil {
		return err
	}
	switch source.Kind {
	case model.KindDesign, model.KindPlan, model.KindReplan:
	default:
		return nil
	}
	if card.Status == model.CardFailed || card.Status == model.CardCanceled || skippedOutcome(outcome, card.Status) || designUnusable(text, outcome) {
		return d.markNeedsInput(ctx, source.ID)
	}
	return d.finishNormalizedSource(ctx, obj, source, text)
}

// finishNormalizedSource applies a usable normalize extraction and closes the
// source. No recorded design-file change becomes done; merge_queued and merged
// stay as they are. The source attempt is consumed without reading its result.
func (d Deps) finishNormalizedSource(ctx context.Context, obj model.Objective, source model.Card, text string) error {
	if judgeResultFreeze(text) == freezeUnclear {
		if err := d.markNeedsInput(ctx, source.ID); err != nil {
			return err
		}
		return d.consumeSourceAttempt(ctx, source.ID)
	}
	if err := d.applyDesign(ctx, obj, source, text); err != nil {
		return err
	}
	held, err := d.applyCardList(ctx, obj, source, text)
	if err != nil {
		return err
	}
	if held {
		return d.consumeSourceAttempt(ctx, source.ID)
	}
	if err := d.markPassedDone(ctx, source, model.OutcomePassed); err != nil {
		return err
	}
	if err := d.markAbsorbed(ctx, source.ID, nil); err != nil {
		return err
	}
	return d.consumeSourceAttempt(ctx, source.ID)
}

func (d Deps) consumeSourceAttempt(ctx context.Context, cardID string) error {
	attemptID, err := d.currentAttemptID(ctx, cardID)
	if err != nil {
		return err
	}
	return d.markAttempt(ctx, attemptID)
}

// applyCardList materializes the plan or replan 新卡 list on the design-phase
// absorb. Prose with no list is left to applyDesign. A list that cannot be
// read marks the source needs_input and reports held.
func (d Deps) applyCardList(ctx context.Context, obj model.Objective, source model.Card, text string) (bool, error) {
	if source.Kind != model.KindPlan && source.Kind != model.KindReplan {
		return false, nil
	}
	_, hasList := sectionBody(text, "新卡")
	cards, err := d.Store.ListCards(ctx, obj.ID)
	if err != nil {
		return false, err
	}
	known := map[string]bool{}
	for _, c := range cards {
		known[c.ID] = true
	}
	list := ParseCardList(text, source.Node, known)
	if list.Bad {
		if !hasList {
			return false, nil
		}
		if err := d.markNeedsInput(ctx, source.ID); err != nil {
			return false, err
		}
		return true, nil
	}
	switch source.Kind {
	case model.KindPlan:
		err = d.materializePlan(ctx, obj, source, list)
	default:
		err = d.materializeReplan(ctx, obj, source, list, cards)
	}
	return false, err
}

func (d Deps) materializePlan(ctx context.Context, obj model.Objective, source model.Card, list CardList) error {
	if _, ok, err := d.recordedPlanSplit(ctx, source.ID); err != nil {
		return err
	} else if ok {
		return nil
	}
	if err := d.hangListQuestions(ctx, source.Node, list.Questions); err != nil {
		return err
	}
	var creates []ListedCard
	for _, draft := range list.Cards {
		if draft.Op == "delete" || draft.Outside {
			continue
		}
		creates = append(creates, draft)
	}
	ids, err := d.insertListed(ctx, obj, source.Node, creates)
	if err != nil {
		return err
	}
	return d.notePlanSplit(ctx, source.ID, source.Node, ids)
}

type planSplitBody struct {
	Node  string   `json:"node"`
	Cards []string `json:"cards"`
}

func (d Deps) recordedPlanSplit(ctx context.Context, planID string) (planSplitBody, bool, error) {
	if d.Store == nil || strings.TrimSpace(planID) == "" {
		return planSplitBody{}, false, nil
	}
	evs, err := d.Store.ListEvents(ctx, "card:"+planID)
	if err != nil {
		return planSplitBody{}, false, err
	}
	for _, ev := range evs {
		if ev.Type != "plan_split" || len(ev.Data) == 0 {
			continue
		}
		var body planSplitBody
		if json.Unmarshal(ev.Data, &body) != nil {
			continue
		}
		return body, true, nil
	}
	return planSplitBody{}, false, nil
}

func (d Deps) notePlanSplit(ctx context.Context, planID, node string, ids []string) error {
	if len(ids) == 0 || strings.TrimSpace(planID) == "" {
		return nil
	}
	raw, err := json.Marshal(planSplitBody{Node: node, Cards: ids})
	if err != nil {
		return err
	}
	return d.noteEvent(ctx, "card:"+planID, "plan_split", raw)
}

func (d Deps) materializeReplan(ctx context.Context, obj model.Objective, source model.Card, list CardList, cards []model.Card) error {
	started := map[string]bool{}
	for _, c := range cards {
		attempts, err := d.Store.ListAttempts(ctx, c.ID)
		if err != nil {
			return err
		}
		if len(attempts) > 0 {
			started[c.ID] = true
		}
	}
	change := PlanReplan(cards, started, source.Node, list)
	if err := d.hangListQuestions(ctx, source.Node, change.Questions); err != nil {
		return err
	}
	taken := map[string]bool{}
	for _, c := range cards {
		taken[c.ID] = true
	}
	creates, edits, err := bindCreatedCards(change.Create, change.Edit, taken)
	if err != nil {
		return err
	}
	if _, err := d.insertBound(ctx, obj, source.Node, creates); err != nil {
		return err
	}
	for _, draft := range edits {
		current, err := d.Store.GetCard(ctx, draft.Key)
		if err != nil {
			return err
		}
		current.Kind = draft.Kind
		current.Title = draft.Title
		current.Body = draft.Body
		current.Tier = draft.Tier
		current.Deps = draft.Deps
		current.Acceptance = draft.Acceptance
		current.Claims = draft.Claims
		if err := d.Store.UpdateCard(ctx, &current); err != nil {
			return err
		}
	}
	for _, id := range change.Delete {
		if err := d.Store.DeleteCard(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (d Deps) insertListed(ctx context.Context, obj model.Objective, node string, creates []ListedCard) ([]string, error) {
	if len(creates) == 0 {
		return nil, nil
	}
	cards, err := d.Store.ListCards(ctx, obj.ID)
	if err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	for _, c := range cards {
		taken[c.ID] = true
	}
	bound, _, err := bindCreatedCards(creates, nil, taken)
	if err != nil {
		return nil, err
	}
	return d.insertBound(ctx, obj, node, bound)
}

func (d Deps) insertBound(ctx context.Context, obj model.Objective, node string, creates []ListedCard) ([]string, error) {
	ids := make([]string, 0, len(creates))
	for _, draft := range creates {
		if draft.ID == "" {
			return nil, errors.New("loop: listed card has no id")
		}
		card := cardFromDraft(obj.ID, node, draft)
		if err := d.addCard(ctx, obj, &card); err != nil {
			return nil, err
		}
		if _, err := d.Store.GetCard(ctx, card.ID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return nil, err
		}
		ids = append(ids, card.ID)
	}
	return ids, nil
}

func (d Deps) hangListQuestions(ctx context.Context, nodeID string, questions []ListQuestion) error {
	if strings.TrimSpace(nodeID) == "" || len(questions) == 0 {
		return nil
	}
	node, err := d.Store.GetNode(ctx, nodeID)
	if err != nil {
		return err
	}
	changed := false
	for _, q := range questions {
		text := q.Text
		if q.Node != "" && q.Node != nodeID && !strings.Contains(text, q.Node) {
			text += " (" + q.Node + ")"
		}
		if _, added := addOpenQuestion(&node, text, model.OQDecision); added {
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return d.Store.UpdateNode(ctx, &node)
}

// currentAttemptID is the latest attempt id. It does not read the result file.
func (d Deps) currentAttemptID(ctx context.Context, cardID string) (string, error) {
	if d.Store == nil {
		return "", nil
	}
	attempts, err := d.Store.ListAttempts(ctx, cardID)
	if err != nil {
		return "", err
	}
	cur := currentAttempt(attempts)
	if cur == nil {
		return "", nil
	}
	return cur.ID, nil
}

// backfillSkippedResearch records unanswered when a research card is terminal
// and its latest attempt was already handled as a skip. The voided text is not stored.
// The write is one-shot for that attempt: a later recheck may use the same question id.
func (d Deps) backfillSkippedResearch(ctx context.Context, obj model.Objective, card model.Card) error {
	if card.Kind != model.KindResearch || !terminalCard(card.Status) {
		return nil
	}
	_, base, outcome, attemptID, err := d.harvested(ctx, card)
	if err != nil {
		return err
	}
	if !skippedOutcome(outcome, card.Status) {
		return nil
	}
	return d.disposeSkippedResearch(ctx, obj, card, attemptID, base)
}

// disposeSkippedResearch applies the terminal unanswered write once, and only
// for this card's current attempt. A historical callback, whether already
// consumed or still late, returns without changing question status.
// A skip that is not yet terminal must not call this.
func (d Deps) disposeSkippedResearch(ctx context.Context, obj model.Objective, card model.Card, attemptID, base string) error {
	current, err := d.currentAttemptID(ctx, card.ID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(attemptID) != strings.TrimSpace(current) {
		return nil
	}
	key := skippedResearchKey(card, attemptID)
	done, err := d.hasEvent(ctx, key, evUnanswered)
	if err != nil || done {
		return err
	}
	if err := d.recordSkippedResearch(ctx, obj, card, base); err != nil {
		return err
	}
	return d.noteEvent(ctx, key, evUnanswered, nil)
}

func skippedResearchKey(card model.Card, attemptID string) string {
	if id := strings.TrimSpace(attemptID); id != "" {
		return attemptKey(id)
	}
	return "card:" + card.ID
}

// recoverUnparsed keeps one normalize card tied to the source.
// resolved is true only after that card has failed or its extraction was absorbed.
func (d Deps) recoverUnparsed(ctx context.Context, obj model.Objective, card model.Card) (bool, error) {
	existing, err := d.normalizeFor(ctx, obj.ID, card.ID)
	if err != nil {
		return false, err
	}
	if existing == nil {
		return false, d.addCard(ctx, obj, &model.Card{
			Objective: obj.ID,
			Node:      card.Node,
			Kind:      model.KindNormalize,
			Title:     "整理",
			Body:      "整理 " + card.ID + "\n",
			Tier:      model.TierT1,
			Status:    model.CardReady,
			Deps:      []string{card.ID},
		})
	}
	if !terminalCard(existing.Status) || existing.Status == model.CardCanceled {
		return false, nil
	}
	text, _, outcome, _, err := d.harvested(ctx, *existing)
	if err != nil {
		return false, err
	}
	if existing.Status == model.CardFailed || skippedOutcome(outcome, existing.Status) || designUnusable(text, outcome) {
		return true, d.markNeedsInput(ctx, card.ID)
	}
	if err := d.finishNormalizedSource(ctx, obj, card, text); err != nil {
		return false, err
	}
	return true, nil
}

func (d Deps) normalizeFor(ctx context.Context, objective, cardID string) (*model.Card, error) {
	cards, err := d.Store.ListCards(ctx, objective)
	if err != nil {
		return nil, err
	}
	for i := range cards {
		if cards[i].Kind != model.KindNormalize {
			continue
		}
		if len(cards[i].Deps) > 0 && cards[i].Deps[0] == cardID {
			c := cards[i]
			return &c, nil
		}
	}
	return nil, nil
}

func (d Deps) markPassedDone(ctx context.Context, card model.Card, outcome model.Outcome) error {
	if outcome != model.OutcomePassed {
		return nil
	}
	fresh, err := d.Store.GetCard(ctx, card.ID)
	if err != nil {
		return err
	}
	switch fresh.Status {
	case model.CardDone, model.CardMerged, model.CardMergeQueued:
		return nil
	}
	merge, err := d.designFileChanged(ctx, fresh, card.Claims)
	if err != nil {
		return err
	}
	if merge {
		fresh.Status = model.CardMergeQueued
		fresh.WaitReason = ""
		if len(card.Claims) > 0 {
			fresh.Claims = append([]model.Claim(nil), card.Claims...)
		}
		return d.Store.UpdateCard(ctx, &fresh)
	}
	fresh.Status = model.CardDone
	return d.Store.UpdateCard(ctx, &fresh)
}

// designFileChanged reports a design, plan, or replan harvest that touches the
// node document or context pack. Non-empty harvest claims replace the stored card.
func (d Deps) designFileChanged(ctx context.Context, fresh model.Card, harvest []model.Claim) (bool, error) {
	switch fresh.Kind {
	case model.KindDesign, model.KindPlan, model.KindReplan:
	default:
		return false, nil
	}
	obj, err := d.Store.GetObjective(ctx, fresh.Objective)
	if err != nil {
		return false, err
	}
	var doc string
	if fresh.Node != "" {
		node, err := d.Store.GetNode(ctx, fresh.Node)
		if err != nil {
			return false, err
		}
		doc = node.DocPath
	}
	dir, err := d.designDir(ctx, obj)
	if err != nil {
		return false, err
	}
	judged := fresh
	if len(harvest) > 0 {
		judged.Claims = harvest
	}
	return recordsDesignFile(judged, doc, dir+"/"+obj.ID+"/context.md"), nil
}

func skippedOutcome(outcome model.Outcome, status model.CardStatus) bool {
	switch outcome {
	case model.OutcomePassed, model.OutcomeResultUnparsable:
		return false
	case "":
		return status == model.CardFailed || status == model.CardCanceled
	default:
		return true
	}
}

func manualKind(kind model.CardKind) bool {
	switch kind {
	case model.KindDesign, model.KindPlan, model.KindReplan, model.KindResearch, model.KindSpike:
		return true
	default:
		return false
	}
}

func attemptKey(id string) string {
	return "attempt:" + strings.TrimSpace(id)
}

func (d Deps) attemptHandled(ctx context.Context, attemptID string) (bool, error) {
	if strings.TrimSpace(attemptID) == "" {
		return false, nil
	}
	return d.hasEvent(ctx, attemptKey(attemptID), evAbsorbed)
}

func (d Deps) markAttempt(ctx context.Context, attemptID string) error {
	if strings.TrimSpace(attemptID) == "" {
		return nil
	}
	return d.noteEvent(ctx, attemptKey(attemptID), evAbsorbed, nil)
}

func (d Deps) markNeedsInput(ctx context.Context, cardID string) error {
	fresh, err := d.Store.GetCard(ctx, cardID)
	if err != nil {
		return err
	}
	if fresh.Status != model.CardNeedsInput {
		fresh.Status = model.CardNeedsInput
		if err := d.Store.UpdateCard(ctx, &fresh); err != nil {
			return err
		}
	}
	return d.markAbsorbed(ctx, cardID, nil)
}

func (d Deps) markContinued(ctx context.Context, cardID string) error {
	return d.noteEvent(ctx, "card:"+cardID, evContinued, nil)
}

// continuationSettled skips a research card whose successor plan already exists,
// unless a later attempt on that card has not been handled.
func (d Deps) continuationSettled(ctx context.Context, card model.Card) (bool, error) {
	continued, err := d.hasEvent(ctx, "card:"+card.ID, evContinued)
	if err != nil || !continued {
		return false, err
	}
	_, _, outcome, attemptID, err := d.harvested(ctx, card)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(attemptID) == "" {
		return true, nil
	}
	handled, err := d.attemptHandled(ctx, attemptID)
	if err != nil {
		return false, err
	}
	if !handled && !skippedOutcome(outcome, card.Status) {
		return false, nil
	}
	return true, nil
}

func (d Deps) absorbAttempt(ctx context.Context, obj model.Objective, card model.Card, attemptID, base, text string, outcome model.Outcome) error {
	if strings.TrimSpace(attemptID) != "" {
		handled, err := d.attemptHandled(ctx, attemptID)
		if err != nil {
			return err
		}
		if handled {
			if card.Kind == model.KindResearch && terminalCard(card.Status) && skippedOutcome(outcome, card.Status) {
				if err := d.disposeSkippedResearch(ctx, obj, card, attemptID, base); err != nil {
					return err
				}
			}
			return d.settleFrozenResearch(ctx, obj)
		}
	}
	if skippedOutcome(outcome, card.Status) {
		if card.Kind == model.KindResearch && terminalCard(card.Status) {
			if err := d.disposeSkippedResearch(ctx, obj, card, attemptID, base); err != nil {
				return err
			}
		}
		if card.Kind == model.KindNormalize {
			if err := d.applyNormalize(ctx, obj, card, text, outcome); err != nil {
				return err
			}
		}
		if err := d.markAttempt(ctx, attemptID); err != nil {
			return err
		}
		return d.settleFrozenResearch(ctx, obj)
	}
	switch card.Kind {
	case model.KindResearch:
		if card.Node == "" {
			if err := d.absorbInventory(ctx, obj, card, text, base); err != nil {
				return err
			}
			if outcome == model.OutcomePassed {
				if err := d.markPassedDone(ctx, card, outcome); err != nil {
					return err
				}
			}
		} else if err := d.absorbResearch(ctx, obj, card, text, base); err != nil {
			return err
		}
	case model.KindDesign, model.KindPlan, model.KindReplan:
		if designUnusable(text, outcome) || judgeResultFreeze(text) == freezeUnclear {
			resolved, err := d.recoverUnparsed(ctx, obj, card)
			if err != nil {
				return err
			}
			if !resolved {
				return nil
			}
			break
		}
		if err := d.applyDesign(ctx, obj, card, text); err != nil {
			return err
		}
		pass := outcome
		if pass == "" {
			pass = model.OutcomePassed
		}
		if err := d.markPassedDone(ctx, card, pass); err != nil {
			return err
		}
		if err := d.markAbsorbed(ctx, card.ID, nil); err != nil {
			return err
		}
	case model.KindSpike:
		if err := d.absorbSpike(ctx, obj, card, text); err != nil {
			return err
		}
	case model.KindDev, model.KindReview:
		if err := d.absorbDevReview(ctx, obj, card, text, outcome); err != nil {
			return err
		}
	case model.KindNormalize:
		if err := d.applyNormalize(ctx, obj, card, text, outcome); err != nil {
			return err
		}
	default:
		if err := d.markAbsorbed(ctx, card.ID, nil); err != nil {
			return err
		}
	}
	if err := d.markAttempt(ctx, attemptID); err != nil {
		return err
	}
	return d.settleFrozenResearch(ctx, obj)
}

func (d Deps) settleFrozenResearch(ctx context.Context, obj model.Objective) error {
	if obj.Mode != model.ModeAuto {
		return nil
	}
	nodes, err := d.Store.ListNodes(ctx, obj.ID)
	if err != nil {
		return err
	}
	cards, err := d.Store.ListCards(ctx, obj.ID)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		if node.Status != model.NodeFrozen {
			continue
		}
		var research []model.Card
		for _, c := range cards {
			if c.Node == node.ID && c.Kind == model.KindResearch {
				research = append(research, c)
			}
		}
		if len(research) == 0 || unfinishedResearch(cards, node.ID) {
			continue
		}
		settled, err := d.researchSettled(ctx, research)
		if err != nil {
			return err
		}
		if !settled {
			continue
		}
		pending := false
		for _, c := range research {
			continued, err := d.hasEvent(ctx, "card:"+c.ID, evContinued)
			if err != nil {
				return err
			}
			if !continued {
				pending = true
				break
			}
		}
		if !pending {
			continue
		}
		if !readyFollow(cards, node.ID, model.KindPlan) {
			blocked, err := d.unmergedDesignFile(ctx, obj, node, cards)
			if err != nil {
				return err
			}
			if blocked {
				continue
			}
			if err := d.createWorkCard(ctx, obj, node, model.KindPlan, workTitle(model.KindPlan)); err != nil {
				return err
			}
			cards, err = d.Store.ListCards(ctx, obj.ID)
			if err != nil {
				return err
			}
		}
		if !readyFollow(cards, node.ID, model.KindPlan) {
			continue
		}
		for _, c := range research {
			if err := d.markContinued(ctx, c.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// researchSettled is true once every terminal research card's current attempt
// has been handled or skipped. The successor plan waits for that, so its fact
// summary includes every sibling result. A card with no attempt has nothing left to apply.
func (d Deps) researchSettled(ctx context.Context, cards []model.Card) (bool, error) {
	for _, c := range cards {
		_, _, outcome, attemptID, err := d.harvested(ctx, c)
		if err != nil {
			return false, err
		}
		if strings.TrimSpace(attemptID) == "" {
			continue
		}
		handled, err := d.attemptHandled(ctx, attemptID)
		if err != nil {
			return false, err
		}
		if handled || skippedOutcome(outcome, c.Status) {
			continue
		}
		return false, nil
	}
	return true, nil
}

// readyFollow reports a live successor. A finished plan does not consume a later research wave.
func readyFollow(cards []model.Card, nodeID string, kind model.CardKind) bool {
	for _, c := range cards {
		if c.Node != nodeID || c.Kind != kind || terminalCard(c.Status) || c.Status == model.CardPaused || c.Status == model.CardNeedsInput {
			continue
		}
		return true
	}
	return false
}

func (d Deps) designDir(ctx context.Context, obj model.Objective) (string, error) {
	dir := "docs/design"
	if obj.Project != "" && d.Store != nil {
		p, err := d.Store.GetProject(ctx, obj.Project)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(p.DesignDir) != "" {
			dir = strings.TrimSpace(p.DesignDir)
		}
	}
	dir = strings.Trim(strings.ReplaceAll(dir, "\\", "/"), "/")
	if dir == "" {
		dir = "docs/design"
	}
	return dir, nil
}

func recordsDesignFile(card model.Card, docPath, packPath string) bool {
	doc := normPath(docPath)
	pack := normPath(packPath)
	for _, cl := range card.Claims {
		p := normPath(cl.Path)
		if p == "" {
			continue
		}
		if (doc != "" && p == doc) || (pack != "" && p == pack) {
			return true
		}
	}
	return false
}

func (d Deps) unmergedDesignFile(ctx context.Context, obj model.Objective, node model.DesignNode, cards []model.Card) (bool, error) {
	dir, err := d.designDir(ctx, obj)
	if err != nil {
		return false, err
	}
	pack := dir + "/" + obj.ID + "/context.md"
	for _, c := range cards {
		if c.Node != node.ID {
			continue
		}
		switch c.Kind {
		case model.KindDesign, model.KindPlan, model.KindReplan:
		default:
			continue
		}
		if c.Status == model.CardMerged || c.Status == model.CardFailed || c.Status == model.CardCanceled || c.Status == model.CardPaused {
			continue
		}
		if c.Status == model.CardMergeQueued || recordsDesignFile(c, node.DocPath, pack) {
			return true, nil
		}
	}
	return false, nil
}
