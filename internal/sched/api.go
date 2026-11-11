// Package sched is the resident loop that calls the other packages. Design §6.1.
package sched

import (
	"context"
	"errors"
)

// ErrNotImplemented is returned by this package's Stub, used as a test double and fallback.
var ErrNotImplemented = errors.New("not implemented")

// Loop runs one idempotent scheduler pass.
type Loop interface {
	Tick(ctx context.Context) error
}

// Stub is the unimplemented loop.
type Stub struct{}

// Tick is one scheduler pass.
func (Stub) Tick(context.Context) error { return ErrNotImplemented }

// Serve runs Tick until ctx is cancelled and mounts the board.
// The body lives in serve.go.
