// Package claims computes symbol ownership and edit overlap. Design §4.
package claims

import "errors"

// ErrNotImplemented is kept for compatibility; nothing in this package returns it now.
var ErrNotImplemented = errors.New("not implemented")

// Conflict explains which card holds a path or symbol.
type Conflict struct {
	Card   string // 被挡住的卡。
	Other  string // 占用声明的卡。
	Path   string // 冲突路径。
	Symbol string // 冲突符号；文件级冲突为空。
	Reason string // 给人看的原因，例如“被卡 X 占用 f.go:Foo”。
}
