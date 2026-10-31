package loop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"cardex/internal/model"
	"cardex/internal/notify"
	"cardex/internal/question"
	"cardex/internal/store"
	"cardex/internal/workspace"
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
		done, err := d.hasEvent(ctx, "card:"+card.ID, evAbsorbed)
		if err != nil {
			return nil, err
		}
		if done {
			continue
		}
		if skip, err := d.continuationSettled(ctx, card); err != nil {
			return nil, err
		} else if skip {
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
	return d.markAbsorbed(ctx, card.ID, raw)
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
	if parseFreezable(secs["可冻结"]) {
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
		if blocked || !mayReceiveNext(node, cards, questions, researched[node.ID]) {
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
		q, ok := currentFreeze(questions, node.ID, spentFreeze(notes))
		if !ok {
			created, err := d.questions().Create(ctx, model.Question{
				Objective: obj.ID,
				Scope:     node.ID,
				ScopeKind: "node",
				Kind:      model.QuestionFreeze,
				Text:      "冻结 " + string(node.Layer) + " " + node.ID,
				Options:   []string{"同意", "拒绝"},
			})
			if err != nil {
				return err
			}
			q = created
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
	for _, e := range ev {
		if e.Type != evAbsorbed || len(e.Data) == 0 {
			continue
		}
		var q model.OpenQuestion
		if err := json.Unmarshal(e.Data, &q); err != nil {
			return model.OpenQuestion{}, false, err
		}
		if q.ID == "Q0" {
			if q.CitedPaths == nil {
				q.CitedPaths = []string{}
			}
			return q, true, nil
		}
	}
	return model.OpenQuestion{}, false, nil
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
	return d.addCard(ctx, obj, &model.Card{
		Objective: obj.ID,
		Node:      node.ID,
		Kind:      kind,
		Title:     title,
		Body:      body,
		Tier:      model.DefaultTier(kind),
		Status:    model.CardReady,
	})
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
	if err := d.applyDesign(ctx, obj, card, text); err != nil {
		return false, err
	}
	if err := d.markPassedDone(ctx, card, model.OutcomePassed); err != nil {
		return false, err
	}
	return true, d.markAbsorbed(ctx, card.ID, nil)
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
	obj, err := d.Store.GetObjective(ctx, fresh.Objective)
	if err != nil {
		return err
	}
	var doc string
	if fresh.Node != "" {
		node, err := d.Store.GetNode(ctx, fresh.Node)
		if err != nil {
			return err
		}
		doc = node.DocPath
	}
	dir, err := d.designDir(ctx, obj)
	if err != nil {
		return err
	}
	if recordsDesignFile(fresh, doc, dir+"/"+obj.ID+"/context.md") {
		return nil
	}
	fresh.Status = model.CardDone
	return d.Store.UpdateCard(ctx, &fresh)
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
			return d.settleFrozenResearch(ctx, obj)
		}
	}
	if skippedOutcome(outcome, card.Status) {
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
		if designUnusable(text, outcome) {
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
		blocked, err := d.unmergedDesignFile(ctx, obj, node, cards)
		if err != nil {
			return err
		}
		if blocked {
			continue
		}
		if !readyFollow(cards, node.ID, model.KindPlan) {
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

func readyFollow(cards []model.Card, nodeID string, kind model.CardKind) bool {
	for _, c := range cards {
		if c.Node == nodeID && c.Kind == kind && c.Status == model.CardReady {
			return true
		}
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
