//go:build !darwin && !linux && !windows

package roots

import "fmt"

func statVolume(path string) (VolumeInfo, error) {
	return VolumeInfo{}, fmt.Errorf("volume stats are not implemented for %s", path)
}
