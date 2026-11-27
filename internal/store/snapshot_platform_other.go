//go:build !linux

package store

import "os"

const snapshotReadFlags = 0

func prepareStrictSnapshotWatch(string) (*strictSnapshotGuard, error) {
	return nil, ErrSnapshotUnavailable
}

func localSnapshotFD(uintptr) bool            { return false }
func singleSnapshotFileLink(os.FileInfo) bool { return false }
