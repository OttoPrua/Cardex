package runner

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var errCompatibleManaged = errors.New("api channel cannot prove isolation with managed Claude Code policy")

// --bare and --restricted do not disable managed settings. Do not override
// admin policy; refuse to send a card or resolve credentials on that machine.
func requireNoManagedClaudePolicy() error {
	var dirs, files []string
	switch runtime.GOOS {
	case "darwin":
		dirs = []string{"/Library/Application Support/ClaudeCode"}
		const plist = "com.anthropic.claudecode.plist"
		files = append(files, filepath.Join("/Library/Managed Preferences", plist))
		for _, name := range []string{"USER", "LOGNAME"} {
			user := os.Getenv(name)
			if user != "" && filepath.Base(user) == user {
				files = append(files, filepath.Join("/Library/Managed Preferences", user, plist))
			}
		}
	case "linux":
		dirs = []string{"/etc/claude-code"}
		// WSL can inherit managed Windows policy outside this process namespace.
		if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err != nil || isWSLKernel(string(b)) {
			return errCompatibleManaged
		}
	case "windows":
		for _, name := range []string{"ProgramFiles", "ProgramW6432", "ProgramFiles(x86)"} {
			if dir := os.Getenv(name); filepath.IsAbs(dir) {
				dirs = append(dirs, filepath.Join(dir, "ClaudeCode"))
			}
		}
		if len(dirs) == 0 || windowsManagedClaudePolicy() {
			return errCompatibleManaged
		}
	default:
		return errCompatibleManaged
	}
	for _, dir := range dirs {
		for _, name := range []string{"managed-settings.json", "managed-settings.d", "managed-mcp.json"} {
			files = append(files, filepath.Join(dir, name))
		}
	}
	for _, path := range files {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return errCompatibleManaged
		}
	}
	return nil
}

func isWSLKernel(release string) bool {
	return strings.Contains(strings.ToLower(release), "microsoft") ||
		strings.Contains(strings.ToLower(release), "wsl")
}
