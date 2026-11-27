package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/OttoPrua/Cardex/internal/model"
)

// ChannelModelState is the narrow persisted state used by model-list previews.
type ChannelModelState struct {
	ID            string
	Runner        model.RunnerName
	EngineProfile string
	Models        []string
}

func (s *Store) ChannelModels(ctx context.Context, id string) (ChannelModelState, error) {
	var out ChannelModelState
	var raw string
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT id, runner, engine_profile, models FROM channels WHERE id=?`, id).Scan(
		&out.ID, &out.Runner, &out.EngineProfile, &raw,
	); err != nil {
		return ChannelModelState{}, err
	}
	if err := parseJSON(raw, &out.Models); err != nil {
		return ChannelModelState{}, err
	}
	out.Models = normStrings(out.Models)
	return out, nil
}

// ErrCompatibleChannelModelEdit means the channel must be changed through
// the full API binding validation, not the incremental model editor.
var ErrCompatibleChannelModelEdit = errors.New("model previews and incremental edits are not supported for API-bound channels; use channel set --model to validate and stage the API binding")

// CheckIncrementalEdit restricts the preview and update paths alike. Even a
// damaged tagged binding must not be treated as a legacy channel.
func (state ChannelModelState) CheckIncrementalEdit() error {
	if model.HasCompatibleEngineBinding(state.EngineProfile) {
		return ErrCompatibleChannelModelEdit
	}
	return nil
}

// UpdateChannelModels changes only the model list for a legacy channel.
// A caller verifying a preview token must do so in the same outer WithWriteTx.
func (s *Store) UpdateChannelModels(ctx context.Context, id string, models []string) error {
	raw, err := mustJSON(normStrings(models))
	if err != nil {
		return err
	}
	return s.WithWriteTx(ctx, func(tx context.Context) error {
		state, err := s.ChannelModels(tx, id)
		if err != nil {
			return err
		}
		if err := state.CheckIncrementalEdit(); err != nil {
			return err
		}
		result, err := s.q(tx).ExecContext(tx, `UPDATE channels SET models=? WHERE id=?`, raw, id)
		return rowsAffected(result, err)
	})
}

// PreviewChannelModels reads a checkpointed regular DB immutably, never opens
// WAL/SHM, and suppresses a result if its ordinary identity checks changed.
// A concurrent live writer is not supported by preview; it must be quiescent.
// As with a normal filesystem read, access time need not be preserved.
func PreviewChannelModels(ctx context.Context, path, id string) (ChannelModelState, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return ChannelModelState{}, err
	}
	initial, err := os.Lstat(path)
	if err != nil {
		return ChannelModelState{}, err
	}
	if !initial.Mode().IsRegular() {
		return ChannelModelState{}, errors.New("model preview requires a regular checkpointed database file")
	}
	checkpointed := func() error {
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if _, err := os.Lstat(path + suffix); err == nil {
				return errors.New("model preview requires a checkpointed database without active sidecars")
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("check database sidecars: %w", err)
			}
		}
		return nil
	}
	if err := checkpointed(); err != nil {
		return ChannelModelState{}, err
	}
	db, err := sql.Open("sqlite", sqliteURI(path)+"?mode=ro&immutable=1&_pragma=query_only(1)")
	if err != nil {
		return ChannelModelState{}, err
	}
	defer db.Close()
	out, err := (&Store{db: db, path: path}).ChannelModels(ctx, id)
	if err != nil {
		return ChannelModelState{}, err
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(initial, after) || initial.Size() != after.Size() ||
		!initial.ModTime().Equal(after.ModTime()) {
		return ChannelModelState{}, errors.New("checkpointed database changed during model preview")
	}
	if err := checkpointed(); err != nil {
		return ChannelModelState{}, err
	}
	if err := out.CheckIncrementalEdit(); err != nil {
		return ChannelModelState{}, err
	}
	return out, nil
}
