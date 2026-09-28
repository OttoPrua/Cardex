package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// setup is deliberately first-run only: configuration migrations belong to their
// explicit commands, and onboarding must not reset a working queue's policy.
func cmdSetup(args []string) error { return setupWithInput(args, os.Stdin) }

func setupWithInput(args []string, input io.Reader) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	inventory := fs.Bool("inventory", false, "只读盘点本地执行器，不读取认证或调用模型")
	runner := fs.String("runner", "", "claude 或 codex")
	bin := fs.String("bin", "", "执行器路径（默认从 PATH 查找）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("setup 不接受位置参数")
	}
	if *inventory {
		return printSetupInventory()
	}
	root := resolveRoot(*rootFlag)
	if _, err := os.Lstat(configPath(root)); err == nil {
		return fmt.Errorf("配置已存在: %s；setup 不覆盖，请编辑现有配置或使用 -root 新目录", configPath(root))
	} else if !os.IsNotExist(err) {
		return err
	}
	if *runner == "" {
		fmt.Print("选择已安装并登录的 CLI [claude/codex]（回车选择 claude）: ")
		line, err := bufio.NewReader(input).ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			return fmt.Errorf("未选择执行器；非交互调用请加 -runner claude 或 -runner codex")
		}
		*runner = strings.TrimSpace(line)
		if *runner == "" {
			*runner = "claude"
		}
	}
	if *runner != "claude" && *runner != "codex" {
		return fmt.Errorf("不支持 runner %q；请选择 claude 或 codex", *runner)
	}
	if *bin == "" {
		*bin = *runner
	}
	resolved, lookupErr := exec.LookPath(*bin)
	if lookupErr == nil {
		*bin = resolved
	}
	cfg := defaultConfig("claude")
	cfg.DefaultRunner = *runner
	if *runner == "claude" {
		cfg.ClaudeBin = *bin
		// Provider aliases keep a new installation independent of historical pins.
		for typ, td := range cfg.TypeDefaults {
			switch typ {
			case typeSequence:
				td.Model = "sonnet"
			case typeProgressPull:
				td.Model = "haiku"
			default:
				td.Model = "opus"
			}
			td.Effort = "high"
			cfg.TypeDefaults[typ] = td
		}
	} else {
		cfg.CodexBin = *bin
		for tier := range cfg.CodexTierModels {
			// Explicit blank values survive loadConfig's merge with historical defaults.
			cfg.CodexTierModels[tier] = ""
			cfg.CodexTierReasoning[tier] = "medium"
		}
	}
	for _, d := range []string{root, tasksDir(root), archiveDir(root), logsDir(root), workflowsDir(root)} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return err
		}
	}
	if err := writeDefaultTemplates(root); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(configPath(root), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(data, '\n'))
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	fmt.Printf("已配置 %s: %s\n配置: %s\n", *runner, *bin, configPath(root))
	if *runner == "codex" {
		fmt.Println("模型使用 Codex CLI 默认；思考档 medium，可在配置或单张任务中调整。")
	}
	if lookupErr != nil {
		fmt.Printf("尚未找到 CLI: %v\n请安装 %s CLI 并完成登录，再运行 doctor。\n", lookupErr, *runner)
	}
	fmt.Printf("下一步（同一个数据目录）:\n  cardex doctor -root %q\n  cardex add -root %q -dir <项目目录> -dry-run \"你的任务\"\n", root, root)
	fmt.Println("确认任务后去掉 -dry-run 入队，再执行 cardex run <任务ID>；cardex board 打开看板。")
	if runtime.GOOS == "darwin" {
		fmt.Println("后台调度可用 cardex install-launchd；其他平台用 cardex daemon 前台常驻。")
	} else {
		fmt.Println("后台调度可用 cardex daemon 前台常驻（install-launchd 仅用于 macOS）。")
	}
	return nil
}

func doctorNeedsClaude(cfg *Config) bool {
	if cfg.ClaudeBin == "" && len(cfg.Engines) == 0 {
		return false
	}
	if cfg.DefaultRunner == "" || cfg.DefaultRunner == "claude" || len(cfg.Engines) > 0 {
		return true
	}
	for _, runner := range cfg.FallbackOrder {
		if runner == "claude" {
			return true
		}
	}
	return false
}
