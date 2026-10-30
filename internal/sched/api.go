// Package sched is the resident loop that calls the other packages. Design §6.1.
package sched

import (
	"context"
	"errors"
)

// ErrNotImplemented is returned until R3-C fills this package in.
var ErrNotImplemented = errors.New("not implemented")

// Loop runs one idempotent scheduler pass.
type Loop interface {
	Tick(ctx context.Context) error
}

// Stub is the unimplemented loop.
type Stub struct{}

// Tick is one scheduler pass.
func (Stub) Tick(context.Context) error { return ErrNotImplemented }

// Serve runs Tick until ctx is cancelled and mounts the board. R3-C owns the body.
func Serve(ctx context.Context, home, addr string) error { return ErrNotImplemented }
