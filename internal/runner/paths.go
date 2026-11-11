package runner

import (
	"path/filepath"
	"runtime"

	"github.com/OttoPrua/Cardex/internal/proc"
)

// homePath joins the user home with elem. It does not create or read the path.
func homePath(elem ...string) string {
	home, err := proc.UserHome()
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
