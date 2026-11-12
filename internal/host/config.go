package host

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ""
	}
	if cfg.invalid {
		return nil, fmt.Sprintf("engines profile %q unparseable: config.json", name)
	}
	if raw, ok := cfg.Engines[name]; ok {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return nil, fmt.Sprintf("engines profile %q unparseable", name)
		}
		// Same usability rule as runner.ParseEngineProfile. An empty object,
		// unknown fields, or blank known fields are not a profile.
		if _, ok := runner.ParseEngineProfile(trimmed); !ok {
			return nil, fmt.Sprintf("engines profile %q unparseable", name)
		}
		return append([]byte(nil), trimmed...), ""
	}
	if profile, ok := runner.EnginePreset(name); ok {
		out, err := json.Marshal(profile)
		if err != nil {
			return nil, fmt.Sprintf("engines profile %q unparseable: %v", name, err)
		}
		return out, ""
	}
	if cfg.loaded && cfg.Engines != nil {
		return nil, fmt.Sprintf("engines profile %q unknown", name)
	}
	return nil, fmt.Sprintf("engines profile %q missing", name)
}
