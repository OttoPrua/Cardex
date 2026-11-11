// Package merge serializes integration of card branches. Design §4.
package merge

import (
	"context"
	"errors"
)

// ErrNotImplemented is returned by this package's Stub, used as a test double and fallback.
var ErrNotImplemented = errors.New("not implemented")

// Queue is the per-objective merge queue.
type Queue interface {
	Enqueue(ctx context.Context, cardID string) error
	Step(ctx context.Context, objectiveID string) error
}

// Stub is the unimplemented queue.
type Stub struct{}

func (Stub) Enqueue(context.Context, string) error { return ErrNotImplemented }
func (Stub) Step(context.Context, string) error    { return ErrNotImplemented }
