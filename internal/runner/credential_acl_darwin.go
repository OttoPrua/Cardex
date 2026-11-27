package runner

import (
	"errors"
	"os"
	"os/exec"
	"strings"
)

func verifyCredentialACL(path string, before os.FileInfo) error {
	// Darwin named ACL entries can authorize access independently of mode bits.
	// Ask the system tool for metadata only; never read the value into argv.
	cmd := exec.Command("/bin/ls", "-lde", path)
	cmd.Env = []string{"LC_ALL=C"}
	metadata, err := cmd.Output()
	after, statErr := os.Lstat(path)
	if err != nil || statErr != nil || !os.SameFile(before, after) ||
		len(strings.Split(strings.TrimSuffix(string(metadata), "\n"), "\n")) != 1 {
		return errors.New("credential file ACL cannot be verified or has additional access entries")
	}
	return nil
}
