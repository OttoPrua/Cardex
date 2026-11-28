package prompt

import (
	"context"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

const StrongBundleVersion = store.StrongBundleVersion

type StrongBundle = store.StrongBundle

// AssembleStrongBundle exposes the same canonical renderer checked by the
// durable prelaunch claim, so bytes cannot diverge across the boundary.
func AssembleStrongBundle(ctx context.Context, db *store.Store, target model.StrongStage, approvedTaskSHA string) ([]byte, error) {
	return store.AssembleStrongBundle(ctx, db, target, approvedTaskSHA)
}
