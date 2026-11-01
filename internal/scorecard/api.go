// Package scorecard aggregates attempt outcomes by model, kind, and tier. Design §10.
package scorecard

import (
	"context"
	"errors"

	"cardex/internal/model"
)

// ErrNotImplemented is the stub error kept for callers that still name it.
// Compute no longer returns it.
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

// Compute aggregates attempts, cards, and events in home/cardex.db.
func Compute(ctx context.Context, home string) ([]Row, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	db, err := openHome(home)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return aggregate(ctx, db)
}
