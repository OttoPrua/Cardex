// Package scorecard aggregates attempt outcomes by model, kind, and tier. Design §10.
package scorecard

import (
	"context"
	"errors"

	"cardex/internal/model"
)

// ErrNotImplemented is returned until R4-C fills this package in.
var ErrNotImplemented = errors.New("not implemented")

// Row is one model × kind × tier aggregate.
type Row struct {
	Model          string         // 模型。
	Kind           model.CardKind // 卡种类。
	Tier           model.Tier     // 等级。
	ReviewPassRate float64        // 复核通过率。
	MergeRate      float64        // 合入率。
	AvgRework      float64        // 平均返工次数。
	TokensPerMerge float64        // 每张合入卡花费的 token。
	Sample         int            // 样本数。
	Insufficient   bool           // 样本少于 5。
}

// Compute aggregates attempts and events under home.
func Compute(ctx context.Context, home string) ([]Row, error) {
	return nil, ErrNotImplemented
}
