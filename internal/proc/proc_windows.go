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
	shMu   sync.Mutex
	shPath string
	// startPath is PATH as cardex started, so sh.exe is still found after a
	// caller (or a test) narrows PATH for a child.
	startPath = os.Getenv("PATH")
)

// lookIn is exec.LookPath against a given PATH list.
func lookIn(list, name string) string {
	for _, dir := range filepath.SplitList(list) {
		if dir == "" {
			continue
		}
		for _, ext := range []string{".exe", ""} {
			p := filepath.Join(dir, name+ext)
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
				return p
			}
		}
	}
	return ""
}

// gitSh finds sh.exe: CARDEX_SH, then PATH, then next to git.exe (Git for
// Windows keeps it in usr\bin, which is not on PATH by default), then the
// default Git for Windows install dirs. A hit is cached; a miss is retried,
// since PATH can change during the process.
func gitSh() string {
	shMu.Lock()
	defer shMu.Unlock()
	if shPath != "" {
		return shPath
	}
	shPath = findSh()
	return shPath
}

func findSh() string {
	if p := strings.TrimSpace(os.Getenv("CARDEX_SH")); p != "" {
		return p
	}
	if p, err := exec.LookPath("sh"); err == nil {
		return p
	}
	if p := lookIn(startPath, "sh"); p != "" {
		return p
	}
	var cands []string
	git, err := exec.LookPath("git")
	if err != nil {
		if p := lookIn(startPath, "git"); p != "" {
			git, err = p, nil
		}
	}
	if err == nil {
		root := filepath.Dir(filepath.Dir(git)) // …\Git\cmd\git.exe -> …\Git
		cands = append(cands,
			filepath.Join(root, "usr", "bin", "sh.exe"),
			filepath.Join(root, "bin", "sh.exe"),
			filepath.Join(filepath.Dir(root), "usr", "bin", "sh.exe"), // …\mingw64\bin\git.exe
		)
	}
	for _, env := range []string{"ProgramFiles", "ProgramW6432", "ProgramFiles(x86)", "LOCALAPPDATA"} {
		base := os.Getenv(env)
		if base == "" {
			continue
		}
		if env == "LOCALAPPDATA" {
			base = filepath.Join(base, "Programs")
		}
		cands = append(cands, filepath.Join(base, "Git", "usr", "bin", "sh.exe"), filepath.Join(base, "Git", "bin", "sh.exe"))
	}
	for _, c := range cands {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
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

func scriptCmd(cmd *exec.Cmd) {
	// exec.Command already failed LookPath on an extensionless script; the
	// original name is still in Args[0].
	if cmd.Err != nil && len(cmd.Args) > 0 {
		cmd.Path = cmd.Args[0]
	}
	if !strings.ContainsAny(cmd.Path, `/\:`) {
		// A bare name LookPath could not resolve: an extensionless script on PATH.
		if p := findScript(cmd.Path, cmd.Env); p != "" {
			cmd.Path = p
		}
	}
	switch strings.ToLower(filepath.Ext(cmd.Path)) {
	case ".exe", ".com", ".bat", ".cmd":
		return
	}
	if !hasShebang(cmd.Path) {
		return
	}
	sh := gitSh()
	if sh == "" {
		return
	}
	args := []string{sh, filepath.ToSlash(cmd.Path)}
	if len(cmd.Args) > 1 {
		args = append(args, cmd.Args[1:]...)
	}
	cmd.Path = sh
	cmd.Args = args
	cmd.Err = nil
}

// findScript looks for an extensionless "#!" file called name on PATH (from env
// when it sets PATH, else the process PATH).
func findScript(name string, env []string) string {
	path := os.Getenv("PATH")
	for _, kv := range env {
		if len(kv) > 5 && strings.EqualFold(kv[:5], "PATH=") {
			path = kv[5:]
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && hasShebang(p) {
			return p
		}
	}
	return ""
}

// Tool finds a POSIX tool such as false or sleep: PATH first, then the Git for
// Windows usr\bin directory that holds sh.exe.
func Tool(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	if sh := gitSh(); sh != "" {
		p := filepath.Join(filepath.Dir(sh), name+".exe")
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}
