package harvest

import (
	"context"

	"github.com/OttoPrua/Cardex/internal/strongevidence"
)

type StrongSnapshotFile = strongevidence.StrongSnapshotFile

func ReadStrongSnapshot(ctx context.Context, bare, commit string, paths []string) ([]StrongSnapshotFile, error) {
	return strongevidence.ReadStrongSnapshot(ctx, bare, commit, paths)
}

func strongGit(ctx context.Context, dir string, limit int, args ...string) ([]byte, error) {
	return strongevidence.BoundedGit(ctx, dir, limit, args...)
}
