// Package workspace creates and commits per-attempt git worktrees. Design §4.
package workspace

import (
	"context"
	"errors"
)

// ErrNotImplemented is returned by this package's Stub, used as a test double and fallback.
var ErrNotImplemented = errors.New("not implemented")

// CreateRequest describes the worktree for one attempt.
type CreateRequest struct {
	CardID            string // 卡标识。
	AttemptID         string // 尝试标识。
	ObjectiveID       string // 目标标识。
	RepoPath          string // 主 checkout。
	WorktreeRoot      string // worktree 根目录。
	IntegrationBranch string // 集成分支。
	Continue          bool   // 为真时从卡分支 HEAD 续做，否则从集成分支 HEAD 切出。
}

// Worktree is a created attempt checkout.
type Worktree struct {
	Path       string // worktree 路径，<worktree_root>/<card>-<attempt>。
	Branch     string // 卡分支，cardex/<objective>/c/<card>。
	BaseCommit string // 起点提交。
}

// Diff is the patch produced in a worktree.
type Diff struct {
	Patch string   // 统一 diff 文本。
	Files []string // 改动的路径。
}

// Manager owns worktree creation, commit, diff, removal, and push blocking.
type Manager interface {
	Create(ctx context.Context, req CreateRequest) (Worktree, error)
	Commit(ctx context.Context, worktree, message string) (string, error)
	Diff(ctx context.Context, worktree, base string) (Diff, error)
	Remove(ctx context.Context, worktree string) error
	BlockPush(ctx context.Context, worktree string) error
}

// Stub is the unimplemented manager.
type Stub struct{}

func (Stub) Create(context.Context, CreateRequest) (Worktree, error) {
	return Worktree{}, ErrNotImplemented
}
func (Stub) Commit(context.Context, string, string) (string, error) {
	return "", ErrNotImplemented
}
func (Stub) Diff(context.Context, string, string) (Diff, error) {
	return Diff{}, ErrNotImplemented
}
func (Stub) Remove(context.Context, string) error    { return ErrNotImplemented }
func (Stub) BlockPush(context.Context, string) error { return ErrNotImplemented }
