package host

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/OttoPrua/Cardex/internal/roots"
)

func TestReapRunScratchChecksOwnership(t *testing.T) {
	root, evidence := t.TempDir(), t.TempDir()
	dir, cleanup, err := roots.PrepareAttemptScratch(root, "a1")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	other, cleanOther, err := roots.PrepareAttemptScratch(root, "a1")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanOther()
	if err := os.WriteFile(filepath.Join(evidence, scratchRecord), []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reapRunScratch(evidence, "a1"); err == nil {
		t.Fatal("removed unowned scratch")
	}
	if err := os.Remove(filepath.Join(evidence, scratchRecord)); err != nil {
		t.Fatal(err)
	}
	if err := recordRunScratch(evidence, dir); err != nil {
		t.Fatal(err)
	}
	if err := reapRunScratch(evidence, "a1"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, filepath.Join(evidence, scratchRecord)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("scratch state remains at %s: %v", path, err)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal(err)
	}
}
