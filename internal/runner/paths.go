package runner

import (
	"os"
	"path/filepath"
	"runtime"
)

// homePath joins the user home with elem. It does not create or read the path.
func homePath(elem ...string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(append([]string{home}, elem...)...)
}

func cachePath(name string) string {
	if runtime.GOOS == "darwin" {
		return homePath("Library", "Caches", name)
	}
	return homePath(".cache", name)
}

func nonempty(paths ...string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
