// Package loop advances objectives through design and development. Design §6.
package loop

import (
	"context"
	"errors"
)

// ErrNotImplemented is returned until R3-A and R3-B fill this package in.
var ErrNotImplemented = errors.New("not implemented")

// Engine advances one objective by one idempotent step.
type Engine interface {
	Step(ctx context.Context, objectiveID string) error
}

// Stub is the unimplemented engine.
type Stub struct{}

// Step is the combined design and development step.
func (Stub) Step(context.Context, string) error { return ErrNotImplemented }

// DesignStep is the design-phase entry R3-A owns.
func DesignStep(ctx context.Context, objectiveID string) error { return ErrNotImplemented }

// DevStep is the development-phase entry R3-B owns.
// DevStep is the development-phase entry R3-B owns.
// One call disposes the objective's harvested attempts. A second call on the
// same stored state does not create another card, question, notification, or
// channel change. The store is $CARDEX_HOME/cardex.db.
func DevStep(ctx context.Context, objectiveID string) error {
	return devStep(ctx, objectiveID)
}
