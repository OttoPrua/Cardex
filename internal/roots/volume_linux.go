//go:build linux

package roots

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func statVolume(path string) (VolumeInfo, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return VolumeInfo{}, err
	}
	if st.Bsize < 0 {
		return VolumeInfo{}, os.ErrInvalid
	}
	bsize := uint64(st.Bsize)
	return VolumeInfo{
		Mount: linuxMount(path),
		Free:  st.Bavail * bsize,
		Total: st.Blocks * bsize,
	}, nil
}

func linuxMount(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	best := ""
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		mnt := fields[4]
		if abs == mnt || strings.HasPrefix(abs, strings.TrimRight(mnt, "/")+"/") {
			if len(mnt) > len(best) {
				best = mnt
			}
		}
	}
	return best
}
