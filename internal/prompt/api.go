// Package prompt builds the executor prompt in a stable order. Design §8.
package prompt

import "github.com/OttoPrua/Cardex/internal/model"

// Input is the material Build concatenates.
type Input struct {
	Card       model.Card           // 卡正文、声明和验收命令。
	Objective  model.Objective      // 目标，用于定位上下文包。
	Nodes      []model.DesignNode   // 相关设计节点。
	Facts      []model.OpenQuestion // 事实摘要。仅 design、plan、replan 渲染，含目标的 Q0。
	Context    string               // 上下文包正文；空则从集成分支的提交读取。
	ReworkNote string               // 返工意见；没有则为空。
	Repo       string               // 读取 context.md 和节点文档的仓库。
	Revision   string               // 读取文档所用的提交，通常是集成分支 HEAD。
	DesignDir  string               // 设计文档目录；空则 docs/design/。
}
