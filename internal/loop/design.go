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
	if err := d.dispatchResearch(ctx, obj); err != nil {
		return err
	}
	if err := d.followResearch(ctx, obj, researched); err != nil {
		return err
	}
	if err := d.advanceNodes(ctx, obj); err != nil {
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
	switch card.Kind {
	case model.KindResearch:
		if card.Node == "" {
			return d.absorbInventory(ctx, obj, card)
		}
		if err := d.absorbResearch(ctx, obj, card); err != nil {
			return err
		}
		researched[card.Node] = true
		return nil
	case model.KindDesign, model.KindPlan, model.KindReplan:
		return d.absorbDesign(ctx, obj, card)
	case model.KindSpike:
		return d.absorbSpike(ctx, obj, card)
	case model.KindDev, model.KindReview:
		return d.absorbDevReview(ctx, obj, card)
	default:
		return d.markAbsorbed(ctx, card.ID, nil)
	}
}

func (d Deps) absorbInventory(ctx context.Context, obj model.Objective, card model.Card) error {
	q := model.OpenQuestion{
		ID: "Q0", Type: model.OQFact, Text: "盘点 " + obj.Title,
		Status: model.OQUnanswered, CitedPaths: []string{}, Commit: card.Base,
	}
	secs := splitSections(card.Body)
	if b, ok := parseFactSection(secs["事实"])["Q0"]; ok {
		applyBlock(&q, b, card.Base)
	} else {
		text := strings.TrimSpace(secs["事实"])
		if text == "" {
			text = strings.TrimSpace(secs["结论"])
		}
		if text != "" {
			applyBlock(&q, factBlock{present: true, answer: text, evidence: text}, card.Base)
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

func (d Deps) absorbDesign(ctx context.Context, obj model.Objective, card model.Card) error {
	if card.Node == "" {
		return d.markAbsorbed(ctx, card.ID, nil)
	}
	node, err := d.Store.GetNode(ctx, card.Node)
	if err != nil {
		return err
	}
	secs := splitSections(card.Body)
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
		if err := d.noteEvent(ctx, "node:"+node.ID, evFreezable, nil); err != nil {
			return err
		}
	}
	return d.markAbsorbed(ctx, card.ID, nil)
}

func (d Deps) absorbSpike(ctx context.Context, obj model.Objective, card model.Card) error {
	if card.Node != "" {
		if id := spikeQuestionID(card.Body); id != "" {
			conclusion := strings.TrimSpace(splitSections(card.Body)["结论"])
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

func (d Deps) absorbDevReview(ctx context.Context, obj model.Objective, card model.Card) error {
	secs := splitSections(card.Body)
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
	if card.Kind == model.KindReview && problem == "" && passedReview(secs["复核结论"]) && card.Node != "" {
		if err := d.noteEvent(ctx, "node:"+card.Node, evReviewPass, nil); err != nil {
			return err
		}
	}
	return d.markAbsorbed(ctx, card.ID, nil)
}

func (d Deps) createSpike(ctx context.Context, obj model.Objective, node model.DesignNode, q model.OpenQuestion) error {
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
	return d.createWorkCard(ctx, obj, *child, model.KindDesign, title)
}

func (d Deps) followResearch(ctx context.Context, obj model.Objective, nodes map[string]bool) error {
	if len(nodes) == 0 {
		return nil
	}
	cards, err := d.Store.ListCards(ctx, obj.ID)
	if err != nil {
		return err
	}
	for id := range nodes {
		if id == "" || unfinishedResearch(cards, id) {
			continue
		}
		node, err := d.Store.GetNode(ctx, id)
		if err != nil {
			return err
		}
		if activeWork(cards, id) {
			continue
		}
		kind := followKind(node.Status)
		title := "设计"
		switch kind {
		case model.KindPlan:
			title = "拆卡"
		case model.KindReplan:
			title = "重规划"
		}
		if err := d.createWorkCard(ctx, obj, node, kind, title); err != nil {
			return err
		}
		cards, err = d.Store.ListCards(ctx, obj.ID)
		if err != nil {
			return err
		}
	}
	return nil
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
	if node.Status != model.NodeReopened {
		if err := d.applyReopen(ctx, obj, &node, questions, cards); err != nil {
			return err
		}
	}
	node, err = d.Store.GetNode(ctx, nodeID)
	if err != nil {
		return err
	}
	if node.Status == model.NodeFrozen || node.Status == model.NodeReopened {
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
	blocked := nodeBlocked(node)
	switch decideFreeze(node.Layer, obj.FreezeMode) {
	case freezeNow:
		if blocked {
			return nil
		}
		return d.freezeNode(ctx, obj, node)
	case freezeAsk:
		q, ok := freezeQuestion(questions, node.ID)
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
		if !hasKind(cards, node.ID, model.KindReview) {
			if err := d.addCard(ctx, obj, &model.Card{
				Objective: obj.ID,
				Node:      node.ID,
				Kind:      model.KindReview,
				Title:     "复核 " + node.ID,
				Body:      "复核节点 " + node.ID + "\n",
				Tier:      model.TierT3,
				Status:    model.CardReady,
			}); err != nil {
				return err
			}
		}
		ok, err := d.hasEvent(ctx, "node:"+node.ID, evReviewPass)
		if err != nil {
			return err
		}
		if blocked || !ok {
			return nil
		}
		return d.freezeNode(ctx, obj, node)
	default:
		return nil
	}
}

func (d Deps) applyReopen(ctx context.Context, obj model.Objective, node *model.DesignNode, questions []model.Question, cards []model.Card) error {
	var q model.Question
	found := false
	for _, item := range questions {
		if item.Kind == model.QuestionReopen && item.Scope == node.ID && consented(item) {
			q = item
			found = true
			break
		}
	}
	if !found || !consented(q) {
		return nil
	}
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
	if hasKind(cards, node.ID, model.KindReplan) {
		return nil
	}
	return d.createWorkCard(ctx, obj, *node, model.KindReplan, "重规划")
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

func freezeQuestion(questions []model.Question, nodeID string) (model.Question, bool) {
	for _, q := range questions {
		if q.Kind == model.QuestionFreeze && q.Scope == nodeID {
			return q, true
		}
	}
	return model.Question{}, false
}

func (d Deps) ensureDirection(ctx context.Context, obj model.Objective) error {
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
	if c.Status == "" {
		c.Status = model.CardReady
	}
	if c.Tier == "" || c.Tier == model.TierNone {
		c.Tier = model.DefaultTier(c.Kind)
	}
	if c.Kind == model.KindResearch && c.Base == "" {
		c.Base = d.head(ctx, obj)
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
