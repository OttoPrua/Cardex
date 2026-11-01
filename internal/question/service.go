package question

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	if err := s.store.CreateQuestion(ctx, &q); err != nil {
		return model.Question{}, err
	}
	if s.deliver == nil {
		return q, nil
	}
	origin := ""
	if q.Objective != "" {
		if obj, err := s.store.GetObjective(ctx, q.Objective); err == nil {
			origin = obj.Origin
		}
	}
	if err := s.deliver.Deliver(ctx, notify.Message{
		QuestionID: q.ID,
		Objective:  q.Objective,
		Origin:     origin,
		Text:       q.Text,
	}); err != nil {
		return q, err
	}
	return q, nil
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
