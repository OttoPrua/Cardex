package host

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/OttoPrua/Cardex/internal/runner"
)

type fileConfig struct {
	Engines map[string]json.RawMessage `json:"engines"`
	// Browser configures the Chrome that browser cards get (see browser.go).
	Browser struct {
		Chrome string `json:"chrome"`
	} `json:"browser"`
	invalid bool
	loaded  bool
}

func loadConfig(home string) fileConfig {
	data, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		return fileConfig{}
	}
	var cfg fileConfig
	if json.Unmarshal(data, &cfg) != nil {
		cfg.invalid = true
		return cfg
	}
	cfg.loaded = true
	return cfg
}

// resolveEngineProfile returns the profile JSON for a non-empty engines name.
// A missing file, an unknown name, and a value that is not a profile object
// produce different reasons. A preset still resolves when the file has no entry.
func resolveEngineProfile(cfg fileConfig, name string) ([]byte, string) {
	return runner.ResolveEngineJSON(cfg.invalid, cfg.loaded, cfg.Engines, name)
}
