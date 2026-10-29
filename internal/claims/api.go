// Package claims computes symbol ownership and edit overlap. Design §4.
package claims

import (
	"errors"

	"cardex/internal/model"
)

// ErrNotImplemented is returned until R2-C fills this package in.
var ErrNotImplemented = errors.New("not implemented")

// Conflict explains which card holds a path or symbol.
type Conflict struct {
	Card   string // 被挡住的卡。
	Other  string // 占用声明的卡。
	Path   string // 冲突路径。
	Symbol string // 冲突符号；文件级冲突为空。
	Reason string // 给人看的原因，例如“被卡 X 占用 f.go:Foo”。
}

// Conflicts reports scheduling conflicts among cards. It does not judge correctness.
func Conflicts(cards []model.Card) ([]Conflict, error) {
	return nil, ErrNotImplemented
}

// ChangedSymbols compares two commits and lists symbols that actually changed.
func ChangedSymbols(repo, before, after string) ([]model.Claim, error) {
	return nil, ErrNotImplemented
}
