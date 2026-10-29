// Package question tracks decisions that need a person. Design §9.
package question

import (
	"context"
	"errors"

	"cardex/internal/model"
)

// ErrNotImplemented is returned until R3-A fills this package in.
var ErrNotImplemented = errors.New("not implemented")

// Service creates, answers, and queries blocking questions.
type Service interface {
	Create(ctx context.Context, q model.Question) (model.Question, error)
	Answer(ctx context.Context, id, option string) (model.Question, error)
	Pending(ctx context.Context, scope string) ([]model.Question, error)
	Blocked(ctx context.Context, scope string) (bool, error)
}

// Stub is the unimplemented service.
type Stub struct{}

func (Stub) Create(context.Context, model.Question) (model.Question, error) {
	return model.Question{}, ErrNotImplemented
}
func (Stub) Answer(context.Context, string, string) (model.Question, error) {
	return model.Question{}, ErrNotImplemented
}
func (Stub) Pending(context.Context, string) ([]model.Question, error) {
	return nil, ErrNotImplemented
}
func (Stub) Blocked(context.Context, string) (bool, error) {
	return false, ErrNotImplemented
}
