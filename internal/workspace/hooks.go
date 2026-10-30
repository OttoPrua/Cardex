package workspace

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
)

//go:embed hooks/pre-push
var prePushHook []byte

// hookDir is the Cardex-owned directory core.hooksPath should point at.
// Prefer the copy shipped in this module. If that path is not on disk
// (trimmed build), materialize the embedded hook outside the user project.
func hookDir() (string, error) {
	if dir, ok := moduleHookDir(); ok {
		hook := filepath.Join(dir, "pre-push")
		if err := os.Chmod(hook, 0o755); err != nil {
			return "", err
		}
		return dir, nil
	}
	sum := sha256.Sum256(prePushHook)
	dir := filepath.Join(os.TempDir(), "cardex-hooks-"+hex.EncodeToString(sum[:8]))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	hook := filepath.Join(dir, "pre-push")
	if err := os.WriteFile(hook, prePushHook, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func moduleHookDir() (string, bool) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", false
	}
	dir := filepath.Join(filepath.Dir(file), "hooks")
	st, err := os.Stat(filepath.Join(dir, "pre-push"))
	if err != nil || st.IsDir() {
		return "", false
	}
	return dir, true
}
