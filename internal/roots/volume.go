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
	cur := path
	if cur == "" {
		cur = "."
	}
	abs, err := filepath.Abs(cur)
	if err == nil {
		cur = abs
	}
	for {
		info, err := statVolume(cur)
		if err == nil {
			return info, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur || !os.IsNotExist(err) {
			return VolumeInfo{}, err
		}
		cur = parent
	}
}
