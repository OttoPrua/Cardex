//go:build darwin

package roots

import "golang.org/x/sys/unix"

func statVolume(path string) (VolumeInfo, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return VolumeInfo{}, err
	}
	bsize := uint64(st.Bsize)
	return VolumeInfo{
		Mount: cString(st.Mntonname[:]),
		Free:  st.Bavail * bsize,
		Total: st.Blocks * bsize,
	}, nil
}

func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
