package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
)

// Presets describe new-card intent only. They never change an admitted task or
// grant access to another provider, and normal admission/routing checks still run.
type DispatchPreset struct {
	Runner      string `json:"runner"`
	Model       string `json:"model,omitempty"`
	Effort      string `json:"effort,omitempty"`
	ReviewAfter bool   `json:"review_after"`
	Note        string `json:"note,omitempty"`
}

var presetNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func validateDispatchPreset(cfg *Config, name string, p DispatchPreset) error {
	if !presetNameRE.MatchString(name) {
		return fmt.Errorf("非法预设名 %q", name)
	}
	if p.Effort != "" && !validEfforts[p.Effort] {
		return fmt.Errorf("预设 %s: 不支持 effort %q", name, p.Effort)
	}
	configured := false
	switch p.Runner {
	case "claude":
		configured = cfg.ClaudeBin != ""
	case "codex":
		configured = cfg.CodexBin != ""
	case "grok-build":
		configured = grokBuildEnabled(cfg)
		if p.Effort == "max" {
			return fmt.Errorf("Grok 不支持 max，请使用实际 CLI 支持的档位")
		}
	case "cursor":
		configured = cursorEnabled(cfg)
		if p.Effort != "" {
			return fmt.Errorf("Cursor 思考档编码在模型 ID 中；preset.effort 请留空")
		}
	case "kimi-cli":
		configured = cfg.KimiCLIBin != ""
	case "agy":
		configured = antigravityEnabled(cfg)
		if p.Effort != "" {
			return fmt.Errorf("agy 思考档请配置 antigravity.effort；preset.effort 请留空")
		}
	case "opencode":
		configured = cfg.OpenCodeBin != ""
	default:
		_, configured = cfg.Engines[p.Runner]
	}
	if !configured {
		return fmt.Errorf("预设 %s 的 runner %q 尚未配置；先配置对应 CLI/订阅", name, p.Runner)
	}
	return nil
}

func applyDispatchPreset(fs *flag.FlagSet, cfg *Config, name string) error {
	if name == "" {
		return nil
	}
	p, ok := cfg.DispatchPresets[name]
	if !ok {
		return fmt.Errorf("未知预设 %q；用 cardex presets 查看或导入", name)
	}
	if err := validateDispatchPreset(cfg, name, p); err != nil {
		return err
	}
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	if explicit["runner"] && fs.Lookup("runner").Value.String() != p.Runner {
		return fmt.Errorf("预设 runner=%s 与 -runner 冲突；请选择对应预设或移除 -preset", p.Runner)
	}
	set := func(key, value string) error {
		if explicit[key] || value == "" {
			return nil
		}
		return fs.Set(key, value)
	}
	if err := set("runner", p.Runner); err != nil {
		return err
	}
	modelFlag := "model"
	effortFlag := "effort"
	switch p.Runner {
	case "codex":
		modelFlag = "codex-model"
	case "grok-build":
		modelFlag, effortFlag = "grok-model", "grok-effort"
	case "cursor":
		modelFlag = "cursor-model"
	case "kimi-cli":
		modelFlag = "kimi-model"
	case "agy":
		modelFlag = "agy-model"
	case "opencode":
		modelFlag = "opencode-model"
	}
	if err := set(modelFlag, p.Model); err != nil {
		return err
	}
	if err := set(effortFlag, p.Effort); err != nil {
		return err
	}
	return set("review-after", strconv.FormatBool(p.ReviewAfter))
}

func cmdPresets(args []string) error {
	fs := flag.NewFlagSet("presets", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	file := fs.String("file", "", "导入预设 JSON 映射；同名更新，其他预设和配置保留")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("presets 不接受位置参数")
	}
	root := resolveRoot(*rootFlag)
	cfg, err := loadConfig(root)
	if err != nil {
		return err
	}
	if *file != "" {
		data, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		var imported map[string]DispatchPreset
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&imported); err != nil {
			return err
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			return fmt.Errorf("预设文件必须只含一个 JSON 对象")
		}
		if len(imported) == 0 {
			return fmt.Errorf("预设映射不能为空")
		}
		for name, p := range imported {
			if err := validateDispatchPreset(cfg, name, p); err != nil {
				return err
			}
		}
		// Raw merge retains private/unknown config fields instead of materializing defaults.
		original, err := os.ReadFile(configPath(root))
		if err != nil {
			return err
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(original, &raw); err != nil {
			return err
		}
		if cfg.DispatchPresets == nil {
			cfg.DispatchPresets = map[string]DispatchPreset{}
		}
		for name, p := range imported {
			cfg.DispatchPresets[name] = p
		}
		raw["dispatch_presets"], err = json.Marshal(cfg.DispatchPresets)
		if err != nil {
			return err
		}
		updated, err := json.MarshalIndent(raw, "", "  ")
		if err != nil {
			return err
		}
		// Keep a byte-exact backup and use a private temporary file (config can contain secrets).
		backup, err := os.CreateTemp(root, "config.before-presets-*.json")
		if err != nil {
			return err
		}
		_, err = backup.Write(original)
		closeErr := backup.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		staged, err := os.CreateTemp(root, ".config-presets-*")
		if err != nil {
			return err
		}
		defer os.Remove(staged.Name())
		_, err = staged.Write(append(updated, '\n'))
		closeErr = staged.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		current, err := os.ReadFile(configPath(root))
		if err != nil {
			return err
		}
		if !bytes.Equal(original, current) {
			return fmt.Errorf("导入期间配置发生变化，未覆盖；请重读后重试")
		}
		if err := os.Rename(staged.Name(), configPath(root)); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "已保存派发预设；原配置备份: %s\n", backup.Name())
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(cfg.DispatchPresets)
}

func printSetupInventory() error {
	type entry struct {
		Runner    string `json:"runner"`
		Command   string `json:"command"`
		Path      string `json:"path,omitempty"`
		Installed bool   `json:"installed"`
		Auth      string `json:"auth"`
	}
	entries := []entry{}
	for _, item := range [][2]string{{"claude", "claude"}, {"codex", "codex"}, {"grok-build", "grok"}, {"cursor", "cursor-agent"}, {"kimi-cli", "kimi"}, {"agy", "agy"}, {"opencode", "opencode"}} {
		path, err := exec.LookPath(item[1])
		if err == nil {
			if abs, e := filepath.Abs(path); e == nil {
				path = abs
			}
		}
		entries = append(entries, entry{item[0], item[1], path, err == nil, "not_checked"})
	}
	out := map[string]any{"os": runtime.GOOS, "arch": runtime.GOARCH, "executors": entries, "next": "在 Codex 或 Hermes 管理会话中使用 cardex-dispatch：说明已有订阅，核实 CLI 登录和模型目录，再保存推荐预设。", "note": "CLI 存在不证明订阅、认证或模型可用；订阅凭据保留在各自官方 CLI 中。"}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
