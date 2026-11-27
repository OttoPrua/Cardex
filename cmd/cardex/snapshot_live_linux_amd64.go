package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/OttoPrua/Cardex/internal/store"
)

const liveSnapshotWorker = "--internal-read-wal-snapshot"

// The worker's only valid entry point is main, before config/PATH handling.
func runLiveSnapshotWorker(args []string) (int, bool) {
	if len(args) == 0 || args[0] != liveSnapshotWorker {
		return 0, false
	}
	if len(args) != 3 {
		return 1, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	data, err := store.ReadLiveSnapshotInWorker(ctx, args[1], args[2])
	switch {
	case errors.Is(err, store.ErrSnapshotSchema):
		return 3, true
	case errors.Is(err, sql.ErrNoRows):
		return 4, true
	case err != nil:
		return 1, true
	}
	if err := json.NewEncoder(os.Stdout).Encode(data); err != nil {
		return 1, true
	}
	return 0, true
}

func tryLiveSnapshot(ctx context.Context, path, card string) (out store.Snapshot, err error) {
	// On Linux the fork child resolves this to the executable inode it
	// already inherited, even if a concurrent upgrade replaced its path.
	cmd := exec.CommandContext(ctx, "/proc/self/exe", liveSnapshotWorker, path, card)
	// Inherit no externally supplied proxy or dynamic loader environment,
	// no file descriptors except standard streams, and no input.
	cmd.Env = []string{"GOMAXPROCS=1"}
	var raw snapshotWorkerOutput
	cmd.Stdout, cmd.Stderr = &raw, io.Discard
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		switch exit.ExitCode() {
		case 3:
			return out, store.ErrSnapshotSchema
		case 4:
			return out, sql.ErrNoRows
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw.buf.Bytes()))
	decoder.DisallowUnknownFields()
	var extra json.RawMessage
	if err != nil || ctx.Err() != nil || decoder.Decode(&out) != nil ||
		decoder.Decode(&extra) != io.EOF || !store.ValidSnapshot(out, card) {
		return store.Snapshot{}, store.ErrSnapshotUnavailable
	}
	return out, nil
}

type snapshotWorkerOutput struct{ buf bytes.Buffer }

func (s *snapshotWorkerOutput) Write(p []byte) (int, error) {
	const max = 4096
	if len(p) > max-s.buf.Len() {
		return 0, store.ErrSnapshotUnavailable
	}
	return s.buf.Write(p)
}
