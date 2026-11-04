package question

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"cardex/internal/model"
	"cardex/internal/notify"
	"cardex/internal/store"
)

// errEmptyAnswer means Answer was called without a chosen option.
// An empty answer is not stored and does not clear Blocked.
var errEmptyAnswer = errors.New("question: empty answer")

// New returns the question service. deliver may be nil in callers that only read;
// Create still stores the question and, when deliver is non-nil, hands it off.
func New(st *store.Store, deliver notify.Deliverer) Service {
	return &svc{store: st, deliver: deliver}
}

type svc struct {
	store   *store.Store
	deliver notify.Deliverer
	after   func(context.Context, model.Question) error
}

// AfterAnswer registers fn on s. fn runs after a non-empty answer is stored.
// A service that is not the one New returns ignores fn. Callers that never
// register still only save the answer. An empty answer is still not stored.
func AfterAnswer(s Service, fn func(context.Context, model.Question) error) {
	impl, ok := s.(*svc)
	if !ok || impl == nil {
		return
	}
	impl.after = fn
}

func (s *svc) Create(ctx context.Context, q model.Question) (model.Question, error) {
	if s.store == nil {
		return model.Question{}, errors.New("question: nil store")
	}
	if strings.TrimSpace(q.Scope) == "" {
		return model.Question{}, errors.New("question: scope required")
	}
	if q.ScopeKind != "card" {
		q.ScopeKind = "node"
	}
	// An unanswered row with the same objective, scope, kind, and text is the
	// stored decision. Reuse it and retry delivery instead of inserting another.
	if existing, ok, err := s.storedDecision(ctx, q); err != nil {
		return model.Question{}, err
	} else if ok {
		if err := s.deliverQuestion(ctx, &existing); err != nil {
			return existing, err
		}
		return existing, nil
	}
	if err := s.store.CreateQuestion(ctx, &q); err != nil {
		return model.Question{}, err
	}
	if err := s.deliverQuestion(ctx, &q); err != nil {
		return q, err
	}
	return q, nil
}

func (s *svc) storedDecision(ctx context.Context, q model.Question) (model.Question, bool, error) {
	all, err := s.store.ListQuestions(ctx, q.Objective)
	if err != nil {
		return model.Question{}, false, err
	}
	text := strings.TrimSpace(q.Text)
	for _, item := range all {
		if item.Objective != q.Objective || item.Scope != q.Scope || item.ScopeKind != q.ScopeKind || item.Kind != q.Kind {
			continue
		}
		if strings.TrimSpace(item.Answer) != "" || strings.TrimSpace(item.Text) != text {
			continue
		}
		return item, true, nil
	}
	return model.Question{}, false, nil
}

func (s *svc) deliverQuestion(ctx context.Context, q *model.Question) error {
	if s.deliver == nil || q == nil || len(q.Deliveries) > 0 {
		return nil
	}
	origin, err := s.questionOrigin(ctx, *q)
	if err != nil {
		return err
	}
	if err := s.deliver.Deliver(ctx, notify.Message{
		QuestionID: q.ID,
		Objective:  q.Objective,
		Origin:     origin,
		Text:       q.Text,
	}); err != nil {
		return err
	}
	q.Deliveries = append(q.Deliveries, model.Delivery{
		At:          time.Now().UTC(),
		Adapter:     "notify",
		Destination: origin,
	})
	return s.store.UpdateQuestion(ctx, q)
}

// questionOrigin uses the card's stored origin event for a card-scoped question.
// A card with no origin, and every node-scoped question, keep the objective origin.
func (s *svc) questionOrigin(ctx context.Context, q model.Question) (string, error) {
	origin := ""
	if q.Objective != "" {
		if obj, err := s.store.GetObjective(ctx, q.Objective); err == nil {
			origin = obj.Origin
		}
	}
	if q.ScopeKind != "card" || strings.TrimSpace(q.Scope) == "" {
		return origin, nil
	}
	evs, err := s.store.ListEvents(ctx, "card:"+q.Scope)
	if err != nil {
		return "", err
	}
	for _, ev := range evs {
		if ev.Type != "origin" {
			continue
		}
		var body struct {
			Origin string `json:"origin"`
		}
		if json.Unmarshal(ev.Data, &body) == nil && strings.TrimSpace(body.Origin) != "" {
			origin = body.Origin
		}
	}
	return origin, nil
}

func (s *svc) Answer(ctx context.Context, id, option string) (model.Question, error) {
	if s.store == nil {
		return model.Question{}, errors.New("question: nil store")
	}
	q, err := s.store.GetQuestion(ctx, id)
	if err != nil {
		return model.Question{}, err
	}
	if strings.TrimSpace(option) == "" {
		return q, errEmptyAnswer
	}
	if len(q.Options) > 0 && !containsOption(q.Options, option) {
		return q, fmt.Errorf("question: option %q is not one of the stored choices", option)
	}
	q.Answer = option
	if err := s.store.UpdateQuestion(ctx, &q); err != nil {
		return model.Question{}, err
	}
	if s.after != nil {
		if err := s.after(ctx, q); err != nil {
			return q, err
		}
	}
	return q, nil
}

func (s *svc) Pending(ctx context.Context, scope string) ([]model.Question, error) {
	if s.store == nil {
		return nil, errors.New("question: nil store")
	}
	all, err := s.store.ListQuestions(ctx, "")
	if err != nil {
		return nil, err
	}
	out := []model.Question{}
	for _, q := range all {
		if q.Scope == scope && strings.TrimSpace(q.Answer) == "" {
			out = append(out, q)
		}
	}
	return out, nil
}

func (s *svc) Blocked(ctx context.Context, scope string) (bool, error) {
	pending, err := s.Pending(ctx, scope)
	if err != nil {
		return false, err
	}
	return len(pending) > 0, nil
}

func containsOption(options []string, option string) bool {
	for _, item := range options {
		if item == option {
			return true
		}
	}
	return false
}
