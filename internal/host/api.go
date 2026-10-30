// Package host runs one attempt in a child process. Design §2 and §12.
package host

import (
	"context"
	"errors"
)

// ErrNotImplemented is returned until R2-A fills this package in.
var ErrNotImplemented = errors.New("not implemented")

// Run hosts the attempt identified by attemptID.
func Run(attemptID string) error {
	return ErrNotImplemented
}

// Reconcile compares live host processes with attempts still marked running.
func Reconcile(ctx context.Context, home string) error {
	return ErrNotImplemented
}
