package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/roots"
	"github.com/OttoPrua/Cardex/internal/store"
)

const scratchRecord = "host-scratch"
const scratchOwner = ".cardex-attempt"

func prepareRunScratch(ctx context.Context, db *store.Store, home string, card model.Card, attemptID, attemptDir string) (string, func(), error) {
	root := roots.Load(home).EffectiveScratch(projectScratch(ctx, db, card))
	dir, cleanup, err := roots.PrepareAttemptScratch(root, attemptID)
	if err != nil || dir == "" {
		return dir, cleanup, err
	}
	if err := recordRunScratch(attemptDir, dir); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, func() {
		if err := reapRunScratch(attemptDir, attemptID); err != nil {
			appendEvent(context.Background(), db, attemptID, "scratch_cleanup_failed", map[string]string{"error": err.Error()})
		}
	}, nil
}

func recordRunScratch(attemptDir, dir string) error {
	if err := os.WriteFile(filepath.Join(dir, scratchOwner), []byte(attemptDir), 0o600); err != nil {
		return err
	}
	path := filepath.Join(attemptDir, scratchRecord)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(dir)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
	}
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// Reconcile can remove this invocation's scratch after the executor group exits.
func reapRunScratch(attemptDir, attemptID string) error {
	record := filepath.Join(attemptDir, scratchRecord)
	raw, err := os.ReadFile(record)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	dir := string(raw)
	if !filepath.IsAbs(dir) || !strings.HasPrefix(filepath.Base(dir), "cardex-"+attemptID+"-") {
		return fmt.Errorf("invalid attempt scratch path %q", dir)
	}
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return os.Remove(record)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("attempt scratch is no longer a directory: %s", dir)
	}
	owner, err := os.ReadFile(filepath.Join(dir, scratchOwner))
	if err != nil {
		return err
	}
	if string(owner) != attemptDir {
		return fmt.Errorf("scratch ownership mismatch for %s", dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Remove(record)
}
