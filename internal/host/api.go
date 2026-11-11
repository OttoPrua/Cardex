// Package host runs one attempt in a child process. Design §2 and §12.
package host

import (
	"context"
	"errors"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/workspace"
)

// ErrNotImplemented is retained for callers that still probe the old stub.
var ErrNotImplemented = errors.New("not implemented")

type workspaceKey struct{}

// WithWorkspace makes Run call mgr instead of the unimplemented workspace stub.
func WithWorkspace(ctx context.Context, mgr workspace.Manager) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, workspaceKey{}, mgr)
}

func workspaceFrom(ctx context.Context) workspace.Manager {
	if ctx != nil {
		if mgr, ok := ctx.Value(workspaceKey{}).(workspace.Manager); ok && mgr != nil {
			return mgr
		}
	}
	return workspace.Stub{}
}

// LiveHost reports an attempt whose host process is still the one to reconcile.
// A dead host is marked by setting EndedAt; the PID is kept for the audit trail.
func LiveHost(a model.Attempt) bool {
	return a.PID > 0 && a.Exit == nil && a.EndedAt.IsZero()
}

// Run hosts the attempt identified by attemptID under home.
func Run(ctx context.Context, home, attemptID string) error {
	return run(ctx, home, attemptID)
}

// Reconcile compares live host processes with attempts still marked running.
func Reconcile(ctx context.Context, home string) error {
	return reconcile(ctx, home)
}
