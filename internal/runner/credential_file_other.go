//go:build !linux && !darwin

package runner

import (
	"errors"
	"os"
	"runtime"
)

// Never treat POSIX mode bits as proof of a Windows ACL.
func openCredentialFile(string, ...os.FileInfo) (*os.File, error) {
	return nil, errors.New("credential file permissions cannot be verified on " + runtime.GOOS)
}
