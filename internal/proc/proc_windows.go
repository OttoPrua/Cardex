//go:build windows

package proc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

const createNoWindow = 0x08000000

var (
	shOnce sync.Once
	shPath string
)

// gitSh finds sh.exe: CARDEX_SH, then PATH, then next to git.exe
// (Git for Windows keeps it in usr\bin, which is not on PATH by default).
func gitSh() string {
	shOnce.Do(func() {
		if p := strings.TrimSpace(os.Getenv("CARDEX_SH")); p != "" {
			shPath = p
			return
		}
		if p, err := exec.LookPath("sh"); err == nil {
			shPath = p
			return
		}
		git, err := exec.LookPath("git")
		if err != nil {
			return
		}
		root := filepath.Dir(filepath.Dir(git)) // …\Git\cmd\git.exe -> …\Git
		for _, c := range []string{
			filepath.Join(root, "usr", "bin", "sh.exe"),
			filepath.Join(root, "bin", "sh.exe"),
			filepath.Join(filepath.Dir(root), "usr", "bin", "sh.exe"), // …\mingw64\bin\git.exe
		} {
			if _, err := os.Stat(c); err == nil {
				shPath = c
				return
			}
		}
	})
	return shPath
}

func shellArgv(line string) (string, []string) {
	if sh := gitSh(); sh != "" {
		return sh, []string{"-c", line}
	}
	return "cmd", []string{"/C", line}
}

// NewGroup puts cmd in a new console process group, so Ctrl-C to cardex does
// not reach it directly. Trees are stopped with taskkill /T.
func NewGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// Detach starts cmd in a new process group with its own hidden console, so it
// outlives the terminal that started cardex.
func Detach(cmd *exec.Cmd) {
	NewGroup(cmd)
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
