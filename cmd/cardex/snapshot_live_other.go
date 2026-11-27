//go:build !linux || !amd64

package main

import (
	"context"

	"github.com/OttoPrua/Cardex/internal/store"
)

func runLiveSnapshotWorker([]string) (int, bool) { return 0, false }

func tryLiveSnapshot(context.Context, string, string) (store.Snapshot, error) {
	return store.Snapshot{}, store.ErrSnapshotUnavailable
}
