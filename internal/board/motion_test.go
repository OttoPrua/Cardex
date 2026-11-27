package board

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"testing"
)

// Use the shipped JS with a fixture clock and mock animations to measure the
// intended deadlines independently of browser frame rates. The board and
// its standard Go tests do not require Node to run.
func TestFolderMotionDeadlines(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable for the motion scheduler fixture")
	}
	template, err := templateFS.ReadFile("templates/page.html")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, filepath.Join("testdata", "motion.mjs"))
	cmd.Stdin = bytes.NewReader(template)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("motion fixture: %s\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}
