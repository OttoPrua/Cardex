//go:build windows

package roots

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func statVolume(path string) (VolumeInfo, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return VolumeInfo{}, err
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return VolumeInfo{}, err
	}
	return VolumeInfo{Mount: volumeName(path), Free: avail, Total: total}, nil
}

func volumeName(path string) string {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return filepath.VolumeName(path)
	}
	buf := make([]uint16, windows.MAX_PATH)
	if err := windows.GetVolumePathName(p, &buf[0], uint32(len(buf))); err != nil {
		return filepath.VolumeName(path)
	}
	return windows.UTF16ToString(buf)
}
