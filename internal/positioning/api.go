// Package positioning stores versioned model placement and proposals. Design §10.
package positioning

import (
	"context"
	"errors"

	"github.com/OttoPrua/Cardex/internal/model"
)

// ErrNotImplemented is returned by Stub. Home is the positioning store.
var ErrNotImplemented = errors.New("not implemented")

// Proposal is a not-yet-approved positioning table.
type Proposal struct {
	ID      string              // 提案标识。
	Entries []model.Positioning // 提案中的定位行。
	Diff    string              // 与当前版本的差异摘要。
}

// Store is the versioned positioning table.
type Store interface {
	Current(ctx context.Context) ([]model.Positioning, error)
	Propose(ctx context.Context, entries []model.Positioning) (Proposal, error)
	Approve(ctx context.Context, proposalID string) error
	Reject(ctx context.Context, proposalID string) error
}

// Stub is the unimplemented positioning store.
type Stub struct{}

func (Stub) Current(context.Context) ([]model.Positioning, error) {
	return nil, ErrNotImplemented
}
func (Stub) Propose(context.Context, []model.Positioning) (Proposal, error) {
	return Proposal{}, ErrNotImplemented
}
func (Stub) Approve(context.Context, string) error { return ErrNotImplemented }
func (Stub) Reject(context.Context, string) error  { return ErrNotImplemented }
