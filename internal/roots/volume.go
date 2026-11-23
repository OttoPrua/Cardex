package roots

import (
	"os"
	"path/filepath"
)

// VolumeInfo is the mount that holds a path and the free space on it.
type VolumeInfo struct {
	Mount string
	Free  uint64
	Total uint64
}

// Volume reports the mount and free space for path.
// A missing path is walked up to the nearest existing parent, so a configured
// directory that has not been created yet still reports the volume it would use.
func Volume(path string) (VolumeInfo, error) {
	cur := stringsTrim(path)
	if cur == "" {
		cur = "."
	}
	abs, err := filepath.Abs(cur)
	if err == nil {
		cur = abs
	}
	var last error
	for i := 0; i < 64; i++ {
		info, err := statVolume(cur)
		if err == nil {
			return info, nil
		}
		last = err
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		if _, serr := os.Stat(parent); serr != nil && !os.IsExist(serr) && i == 0 {
			// Keep walking even when this parent is missing.
		}
		cur = parent
	}
	if last == nil {
		last = os.ErrNotExist
	}
	return VolumeInfo{}, last
}

func stringsTrim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n') {
		s = s[:len(s)-1]
	}
	return s
}
