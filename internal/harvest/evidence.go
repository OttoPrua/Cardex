package harvest

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	evidenceLimit = 1 << 20
	evidenceEdge  = 64 << 10
)

// truncateEvidence stores a body that fits in 1 MiB. A larger body keeps the
// first 64 KiB, one line naming how many original bytes were removed, and the
// last 64 KiB. The inserted line is not part of the omitted count.
func truncateEvidence(in []byte) []byte {
	if len(in) <= evidenceLimit {
		return append([]byte(nil), in...)
	}
	head := in[:evidenceEdge]
	tail := in[len(in)-evidenceEdge:]
	omitted := len(in) - len(head) - len(tail)
	var b bytes.Buffer
	b.Grow(len(head) + len(tail) + 32)
	b.Write(head)
	if len(head) == 0 || head[len(head)-1] != '\n' {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "omitted %d bytes\n", omitted)
	b.Write(tail)
	return b.Bytes()
}

// writeEvidence writes acceptance.log for a harvested attempt and, when the
// result file was present, result.md. home or attemptID empty skips both.
func writeEvidence(home, attemptID string, acceptance []byte, haveResult bool, result []byte) error {
	home = strings.TrimSpace(home)
	attemptID = strings.TrimSpace(attemptID)
	if home == "" || attemptID == "" {
		return nil
	}
	if attemptID == "." || attemptID == ".." || strings.ContainsAny(attemptID, `/\\`) {
		return fmt.Errorf("harvest: attempt id %q", attemptID)
	}
	dir := filepath.Join(home, "attempts", attemptID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var err error
	if werr := os.WriteFile(filepath.Join(dir, "acceptance.log"), truncateEvidence(acceptance), 0o644); werr != nil {
		err = werr
	}
	if haveResult {
		if werr := os.WriteFile(filepath.Join(dir, "result.md"), truncateEvidence(result), 0o644); werr != nil {
			err = errors.Join(err, werr)
		}
	}
	return err
}
