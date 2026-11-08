// Package homeguard reports whether a directory is still a v1 data home.
package homeguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Usage is the command printed when a directory is v1 data.
const Usage = "cardex --home <新目录> import-legacy <这个目录>"

// V1Error means Dir is a v1 data directory: tasks/ holds a .json file and cardex.db is absent.
type V1Error struct {
	Dir string
}

func (e *V1Error) Error() string {
	return fmt.Sprintf("这是 v1 的数据目录 %s。用法：%s", e.Dir, Usage)
}

// IsV1 reports whether err is a v1-data refusal.
func IsV1(err error) bool {
	var v *V1Error
	return errors.As(err, &v)
}

// Check returns a V1Error when dir is v1 data.
// A directory is v1 data when tasks/ contains at least one non-directory .json file
// and cardex.db does not exist. Any other directory, including a missing one, returns nil.
func Check(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	dbInfo, err := os.Stat(filepath.Join(abs, "cardex.db"))
	if err == nil && dbInfo != nil {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	tasks := filepath.Join(abs, "tasks")
	info, err := os.Stat(tasks)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(tasks)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		if strings.HasSuffix(ent.Name(), ".json") {
			return &V1Error{Dir: abs}
		}
	}
	return nil
}
