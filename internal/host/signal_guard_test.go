package host

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestSignalGuard(t *testing.T) {
	var calls int
	prev := signalKill
	signalKill = func(int, syscall.Signal) error {
		calls++
		return nil
	}
	t.Cleanup(func() { signalKill = prev })

	reject := func(name string, fn func() error) {
		t.Helper()
		before := calls
		err := fn()
		if err == nil {
			t.Errorf("rejected %s: want error", name)
		}
		if calls != before {
			t.Errorf("rejected %s: injected syscall called", name)
		}
		t.Logf("rejected %s: %v injected syscall not called", name, err)
	}

	for _, id := range []int{-1, 0, 1} {
		id := id
		label := itoa(id)
		reject(label, func() error { return SignalProcess(id, syscall.SIGKILL) })
		reject("group "+label, func() error { return SignalGroup(id, syscall.SIGKILL) })
	}
	reject("this process", func() error { return SignalProcess(os.Getpid(), syscall.SIGKILL) })
	reject("this process group", func() error { return SignalGroup(syscall.Getpgrp(), syscall.SIGKILL) })
	if calls != 0 {
		t.Fatalf("injected syscall called %d times", calls)
	}

	root := moduleRoot(t)
	needles := []string{
		"syscall" + ".Kill(",
		"unix" + ".Kill(",
		".Signal" + "(",
		".Process" + ".Kill(",
	}
	var hits int
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "internal/host/signal.go" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(body), "\n") {
			for _, needle := range needles {
				if strings.Contains(line, needle) {
					hits++
					t.Errorf("offending file:line %s:%d", rel, i+1)
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if hits == 0 {
		t.Log("source scan clean")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	if n < 0 {
		return "-" + itoa(-n)
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
