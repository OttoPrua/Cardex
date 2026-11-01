// Development step. Design §5 and §6.3–§6.5.
package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cardex/internal/model"
	"cardex/internal/quota"
	"cardex/internal/store"
)

const resultMarker = "\n\n<!-- cardex-result -->\n"

// Notice is one file-adapter delivery.
type Notice struct {
	QuestionID string
	Origin     string
	Text       string
	Kind       string
	Objective  string
}

// Notifier writes a development-loop notification. Tests inject a file stand-in.
type Notifier interface {
	Deliver(ctx context.Context, n Notice) error
}

// FileNotifier writes one JSON file per notice. Destination is the objective origin.
type FileNotifier struct {
	Dir string
}

// Deliver writes the notice as a file named by its question id.
func (f FileNotifier) Deliver(ctx context.Context, n Notice) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.Dir == "" {
		return fmt.Errorf("notification directory is empty")
	}
	if err := os.MkdirAll(f.Dir, 0o755); err != nil {
		return err
	}
	name := n.QuestionID
	if name == "" {
		name = n.Kind
	}
	body, err := json.MarshalIndent(map[string]string{
		"adapter":     "file",
		"origin":      n.Origin,
		"destination": n.Origin,
		"objective":   n.Objective,
		"kind":        n.Kind,
		"text":        n.Text,
		"question_id": n.QuestionID,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(f.Dir, name+".json"), append(body, '\n'), 0o644)
}

type notifierKey struct{}

// WithNotifier returns a context that delivers DevStep notices through n.
func WithNotifier(ctx context.Context, n Notifier) context.Context {
	return context.WithValue(ctx, notifierKey{}, n)
}

func notifierFrom(ctx context.Context, home string) Notifier {
	if n, ok := ctx.Value(notifierKey{}).(Notifier); ok && n != nil {
		return n
	}
	return FileNotifier{Dir: filepath.Join(home, "notifications")}
}

// RestoreQuota replays quota_block events into ledger through quota.ObserveLimit.
func RestoreQuota(ctx context.Context, led *quota.Ledger, events []model.Event) error {
	if led == nil {
		return fmt.Errorf("nil ledger")
	}
	for _, event := range events {
		if event.Type != "quota_block" {
			continue
		}
		var data struct {
			Channel string `json:"channel"`
			Reset   string `json:"reset"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			continue
		}
		reset, err := time.Parse(time.RFC3339Nano, data.Reset)
		if err != nil {
			continue
		}
		if err := led.ObserveLimit(ctx, data.Channel, reset); err != nil {
			return err
		}
	}
	return nil
}

func devStep(ctx context.Context, objectiveID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if objectiveID == "" {
		return fmt.Errorf("objective id required")
	}
	home := os.Getenv("CARDEX_HOME")
	if home == "" {
		return fmt.Errorf("CARDEX_HOME is not set")
	}
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	cfg := LoadConfig(home)
	s, err := loadSnap(ctx, db, objectiveID)
	if err != nil {
		return err
	}
	st := &step{ctx: ctx, db: db, home: home, cfg: cfg, snap: s, now: time.Now().UTC()}
	var notices []Notice
	err = db.WithTx(ctx, func(ctx context.Context) error {
		st.ctx = ctx
		if err := st.warn(); err != nil {
			return err
		}
		if err := st.fillCardBudgets(); err != nil {
			return err
		}
		if err := st.disposeAll(); err != nil {
			return err
		}
		if err := st.advance(); err != nil {
			return err
		}
		if err := st.budgetLines(); err != nil {
			return err
		}
		if err := st.complete(); err != nil {
			return err
		}
		notices = append(notices, st.notices...)
		return nil
	})
	if err != nil {
		return err
	}
	n := notifierFrom(ctx, home)
	for _, notice := range notices {
		if err := n.Deliver(ctx, notice); err != nil {
			return err
		}
	}
	return nil
}

type step struct {
	ctx     context.Context
	db      *store.Store
	home    string
	cfg     Config
	snap    *snap
	notices []Notice
	now     time.Time
}

type snap struct {
	obj       model.Objective
	project   model.Project
	nodes     []model.DesignNode
	cards     []model.Card
	attempts  map[string][]model.Attempt
	questions []model.Question
	channels  []model.Channel
	events    []model.Event
}

func loadSnap(ctx context.Context, db *store.Store, objectiveID string) (*snap, error) {
	obj, err := db.GetObjective(ctx, objectiveID)
	if err != nil {
		return nil, err
	}
	s := &snap{obj: obj, attempts: map[string][]model.Attempt{}}
	if obj.Project != "" {
		project, err := db.GetProject(ctx, obj.Project)
		if err != nil {
			return nil, err
		}
		s.project = project
	}
	s.nodes, err = db.ListNodes(ctx, objectiveID)
	if err != nil {
		return nil, err
	}
	s.cards, err = db.ListCards(ctx, objectiveID)
	if err != nil {
		return nil, err
	}
	for _, card := range s.cards {
		attempts, err := db.ListAttempts(ctx, card.ID)
		if err != nil {
			return nil, err
		}
		s.attempts[card.ID] = attempts
	}
	s.questions, err = db.ListQuestions(ctx, objectiveID)
	if err != nil {
		return nil, err
	}
	s.channels, err = db.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	s.events, err = db.ListEvents(ctx, "")
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (s *snap) card(id string) (model.Card, bool) {
	for _, card := range s.cards {
		if card.ID == id {
			return card, true
		}
	}
	return model.Card{}, false
}

func (s *snap) putCard(card model.Card) {
	for i := range s.cards {
		if s.cards[i].ID == card.ID {
			s.cards[i] = card
			return
		}
	}
	s.cards = append(s.cards, card)
}

func (s *snap) dropCard(id string) {
	next := s.cards[:0]
	for _, card := range s.cards {
		if card.ID != id {
			next = append(next, card)
		}
	}
	s.cards = next
}

func (s *snap) node(id string) (model.DesignNode, bool) {
	for _, node := range s.nodes {
		if node.ID == id {
			return node, true
		}
	}
	return model.DesignNode{}, false
}

func (s *snap) putNode(node model.DesignNode) {
	for i := range s.nodes {
		if s.nodes[i].ID == node.ID {
			s.nodes[i] = node
			return
		}
	}
	s.nodes = append(s.nodes, node)
}

func (s *snap) channel(id string) (model.Channel, bool) {
	for _, ch := range s.channels {
		if ch.ID == id {
			return ch, true
		}
	}
	return model.Channel{}, false
}

func (s *snap) putChannel(ch model.Channel) {
	for i := range s.channels {
		if s.channels[i].ID == ch.ID {
			s.channels[i] = ch
			return
		}
	}
	s.channels = append(s.channels, ch)
}

func (s *snap) disposed(attemptID string) bool {
	return s.hasEvent("attempt:"+attemptID, "dev_disposed")
}

func (s *snap) hasEvent(entity, typ string) bool {
	for _, event := range s.events {
		if event.Entity == entity && event.Type == typ {
			return true
		}
	}
	return false
}

func (s *snap) spend() int64 {
	var n int64
	for _, list := range s.attempts {
		for _, attempt := range list {
			if attempt.Usage.Tokens > 0 {
				n += attempt.Usage.Tokens
			}
		}
	}
	return n
}

func (st *step) event(entity, typ string, data any) error {
	raw := []byte(`{}`)
	if data != nil {
		encoded, err := json.Marshal(data)
		if err != nil {
			return err
		}
		raw = encoded
	}
	event := model.Event{Entity: entity, Type: typ, Data: raw, TS: st.now}
	if err := st.db.AppendEvent(st.ctx, &event); err != nil {
		return err
	}
	st.snap.events = append(st.snap.events, event)
	return nil
}

func (st *step) warn() error {
	if len(st.cfg.Warnings) == 0 || st.snap.hasEvent("objective:"+st.snap.obj.ID, "config_warning") {
		return nil
	}
	return st.event("objective:"+st.snap.obj.ID, "config_warning", map[string]any{"warnings": st.cfg.Warnings})
}

func (st *step) fillBudget(card *model.Card) {
	if card.Budget.SoftTokens == 0 && card.Budget.HardTokens == 0 {
		card.Budget = st.cfg.BudgetFor(card.Kind)
	}
}

func (st *step) fillCardBudgets() error {
	for _, card := range append([]model.Card{}, st.snap.cards...) {
		if card.Budget.SoftTokens != 0 || card.Budget.HardTokens != 0 {
			continue
		}
		st.fillBudget(&card)
		if err := st.saveCard(card); err != nil {
			return err
		}
	}
	return nil
}

func (st *step) saveCard(card model.Card) error {
	if err := st.db.UpdateCard(st.ctx, &card); err != nil {
		return err
	}
	st.snap.putCard(card)
	return nil
}

func (st *step) createCard(card model.Card) (model.Card, error) {
	st.fillBudget(&card)
	if err := st.db.CreateCard(st.ctx, &card); err != nil {
		return model.Card{}, err
	}
	st.snap.putCard(card)
	return card, nil
}

func (st *step) saveObjective() error {
	return st.db.UpdateObjective(st.ctx, &st.snap.obj)
}

func (st *step) dispose(attempt model.Attempt) error {
	return st.event("attempt:"+attempt.ID, "dev_disposed", map[string]string{
		"outcome": string(attempt.Outcome),
		"card":    attempt.Card,
	})
}
