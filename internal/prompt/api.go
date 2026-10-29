// Package prompt builds the executor prompt in a stable order. Design §8.
package prompt

import (
	"context"
	"errors"

	"cardex/internal/model"
)

// ErrNotImplemented is returned until R3-D fills this package in.
var ErrNotImplemented = errors.New("not implemented")

// Input is the material Build concatenates.
type Input struct {
	Card       model.Card         // 卡正文、声明和验收命令。
	Objective  model.Objective    // 目标，用于定位上下文包。
	Nodes      []model.DesignNode // 相关设计节点。
	Context    string             // 上下文包正文；空则从集成分支读取。
	ReworkNote string             // 返工意见；没有则为空。
	Repo       string             // 读取 context.md 和节点文档的仓库。
	Revision   string             // 读取文档所用的提交，通常是集成分支 HEAD。
}

// Build concatenates the kind preface, context pack, node docs, and the card.
func Build(ctx context.Context, in Input) (string, error) {
	return "", ErrNotImplemented
}
