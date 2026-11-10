package repocheck

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type machinePathAllow struct {
	path   string
	reason string
}

func machinePathAllowlist() []machinePathAllow {
	return []machinePathAllow{
		{
			path:   "internal/runner/grok_test.go",
			reason: "quoted home-prefix needle used to reject a machine path, not a stored machine path",
		},
	}
}

func machinePathPrefixes() []string {
	return []string{"/" + "Users" + "/", "/" + "Volumes" + "/"}
}

func TestGitTrackedTextHasNoMachineAbsolutePath(t *testing.T) {
	root := moduleRoot(t)
	allow := machinePathAllowlist()
	for _, ent := range allow {
		if strings.TrimSpace(ent.path) == "" || strings.TrimSpace(ent.reason) == "" {
			t.Fatal("allowlist entry needs a path and a reason")
		}
		if strings.HasPrefix(ent.path, "docs/") || strings.HasPrefix(ent.path, "skills/") {
			t.Fatalf("product or process docs are not allowlisted: %s", ent.path)
		}
	}
	hits, used, err := scanGitTracked(root, allow)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) > 0 {
		t.Fatalf("machine absolute path:\n%s", strings.Join(hits, "\n"))
	}
	for _, ent := range allow {
		if used[ent.path] == 0 {
			t.Fatalf("allowlist entry %s matched no line", ent.path)
		}
	}
}

func TestMachinePathScanReportsFileAndLine(t *testing.T) {
	rel := "sample.txt"
	users := "/" + "Users" + "/example"
	volumes := "/" + "Volumes" + "/example"
	body := "clean\n" + users + "\n" + volumes + "\n"
	hits := scanMachinePaths(rel, body, nil)
	if len(hits) != 2 {
		t.Fatalf("hits %v", hits)
	}
	if !strings.Contains(hits[0], rel+":2:") || !strings.Contains(hits[0], users) {
		t.Fatalf("first hit %s", hits[0])
	}
	if !strings.Contains(hits[1], rel+":3:") || !strings.Contains(hits[1], volumes) {
		t.Fatalf("second hit %s", hits[1])
	}
}

func scanGitTracked(root string, allow []machinePathAllow) ([]string, map[string]int, error) {
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, nil, err
	}
	var hits []string
	used := map[string]int{}
	for _, name := range bytes.Split(out, []byte{0}) {
		if len(name) == 0 {
			continue
		}
		rel := string(name)
		body, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, nil, err
		}
		if bytes.Contains(body, []byte{0}) {
			continue
		}
		fileHits, skipped := scanMachinePathsCounted(rel, string(body), allow)
		hits = append(hits, fileHits...)
		used[rel] += skipped
	}
	return hits, used, nil
}

func scanMachinePaths(rel, body string, allow []machinePathAllow) []string {
	hits, _ := scanMachinePathsCounted(rel, body, allow)
	return hits
}

func scanMachinePathsCounted(rel, body string, allow []machinePathAllow) ([]string, int) {
	var hits []string
	skipped := 0
	for i, line := range strings.Split(body, "\n") {
		if !lineHasMachinePath(line) {
			continue
		}
		if lineAllowed(rel, line, allow) {
			skipped++
			continue
		}
		hits = append(hits, rel+":"+itoa(i+1)+": "+strings.TrimSpace(line))
	}
	return hits, skipped
}

func lineHasMachinePath(line string) bool {
	for _, prefix := range machinePathPrefixes() {
		if strings.Contains(line, prefix) {
			return true
		}
	}
	return false
}

func lineAllowed(rel, line string, allow []machinePathAllow) bool {
	listed := false
	for _, ent := range allow {
		if ent.path == rel && strings.TrimSpace(ent.reason) != "" {
			listed = true
			break
		}
	}
	if !listed {
		return false
	}
	for _, prefix := range machinePathPrefixes() {
		idx := 0
		for {
			i := strings.Index(line[idx:], prefix)
			if i < 0 {
				break
			}
			after := idx + i + len(prefix)
			if after >= len(line) {
				return false
			}
			switch line[after] {
			case '"', '\'', '`':
			default:
				return false
			}
			idx = after
		}
	}
	return true
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
