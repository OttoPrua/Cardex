package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cardex/internal/runner"
)

type fileConfig struct {
	Host struct {
		HardDuration string `json:"hard_duration"`
	} `json:"host"`
	Engines map[string]runner.EngineProfile `json:"engines"`
	invalid bool
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
	return cfg
}

// hardLimit is a wall-clock limit from config.json. Token budgets are not durations.
func hardLimit(cfg fileConfig) time.Duration {
	text := strings.TrimSpace(cfg.Host.HardDuration)
	if text == "" {
		return 0
	}
	d, err := time.ParseDuration(text)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

func profileJSON(cfg fileConfig, name string) ([]byte, bool) {
	if cfg.Engines != nil {
		if p, ok := cfg.Engines[name]; ok {
			raw, err := json.Marshal(p)
			return raw, err == nil
		}
	}
	if p, ok := runner.EnginePreset(name); ok {
		raw, err := json.Marshal(p)
		return raw, err == nil
	}
	return nil, false
}
