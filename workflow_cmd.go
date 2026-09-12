package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func cmdWorkflow(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: cardex workflow init|list|show|writer|goal-run|goal-sync|design-result|design-repair|freeze-candidate|review|ingest-review|repair|try-release-integration|mark ...")
	}
	switch args[0] {
	case "init":
		return cmdWorkflowInit(args[1:])
	case "list":
		return cmdWorkflowList(args[1:])
	case "show":
		return cmdWorkflowShow(args[1:])
	case "writer":
		return cmdWorkflowWriter(args[1:])
	case "goal-run":
		return cmdWorkflowGoalRun(args[1:])
	case "goal-sync":
		return cmdWorkflowGoalSync(args[1:])
	case "design-result":
		return cmdWorkflowDesignResult(args[1:])
	case "design-repair":
		return cmdWorkflowDesignRepair(args[1:])
	case "freeze-candidate":
		return cmdWorkflowFreeze(args[1:])
	case "review":
		return cmdWorkflowReview(args[1:])
	case "ingest-review":
		return cmdWorkflowIngest(args[1:])
	case "repair":
		return cmdWorkflowRepair(args[1:])
	case "try-release-integration":
		return cmdWorkflowTryRelease(args[1:])
	case "mark":
		return cmdWorkflowMark(args[1:])
	default:
		return fmt.Errorf("未知 workflow 子命令 %s", args[0])
	}
}

// workflowTarget resolves the shared `-root <id>` shape used by every
// per-record subcommand.
func workflowTarget(fs *flag.FlagSet, rootFlag *string, usage string) (string, *Config, *WorkflowRecord, error) {
	if fs.NArg() < 1 {
		return "", nil, nil, fmt.Errorf("用法: %s", usage)
	}
	root := resolveRoot(*rootFlag)
	cfg, err := loadConfig(root)
	if err != nil {
		return "", nil, nil, err
	}
	wf, err := loadWorkflow(root, cfg, fs.Arg(0))
	if err != nil {
		return "", nil, nil, err
	}
	return root, cfg, wf, nil
}

// parseWorkflowFlags accepts both `cmd -flag v <id>` and `cmd <id> -flag v`.
// Go's FlagSet stops at the first positional, which would silently drop
// documented shapes like `goal-run <id> -manual`.
func parseWorkflowFlags(fs *flag.FlagSet, args []string) error {
	return fs.Parse(reshapeWorkflowArgs(fs, args))
}

func reshapeWorkflowArgs(fs *flag.FlagSet, args []string) []string {
	takesValue := map[string]bool{}
	if fs != nil {
		fs.VisitAll(func(f *flag.Flag) {
			if f == nil {
				return
			}
			boolish := false
			if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok {
				boolish = bf.IsBoolFlag()
			}
			takesValue[f.Name] = !boolish
		})
	}
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name = name[:eq]
			flags = append(flags, a)
			continue
		}
		flags = append(flags, a)
		if takesValue[name] && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			flags = append(flags, args[i+1])
			i++
		}
	}
	return append(flags, pos...)
}

// firstNonBlank is firstNonEmpty with trimming, so a flag holding only spaces
// falls through to the default instead of becoming a blank identifier.
func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func parseResourceClaims(csv string) ([]ResourceClaim, error) {
	var out []ResourceClaim
	for _, raw := range splitComma(csv) {
		kind, id, ok := strings.Cut(raw, ":")
		if !ok {
			return nil, fmt.Errorf("%w: %q（应为 kind:id）", errWriteDomainUnknownResource, raw)
		}
		out = append(out, ResourceClaim{Kind: strings.TrimSpace(kind), ID: strings.TrimSpace(id)})
	}
	return out, nil
}

func cmdWorkflowInit(args []string) error {
	fs := flag.NewFlagSet("workflow init", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	mode := fs.String("mode", workflowModeSerial, "serial（直派串联）或 federated（联邦模块）")
	moduleID := fs.String("module", "", "模块 ID（小写标识符）")
	goalID := fs.String("goal-id", "", "目标 ID，默认同 -module")
	goal := fs.String("goal", "", "目标陈述")
	parent := fs.String("parent", "", "联邦父 workflow ID（仅 federated）")
	dir := fs.String("dir", "", "模块 worktree")
	repo := fs.String("repo", "", "仓库根，默认取 worktree 的 git top-level")
	criteria := fs.String("terminal-criteria", "", "模块完成标准")
	maxRounds := fs.Int("max-rounds", 3, "最大修复轮次（>=1）")
	writeDomainID := fs.String("write-domain-id", "", "写域 ID，默认同 -module")
	writeDomainLineage := fs.String("write-domain-lineage", "", "写域谱系，默认 <module>-lineage")
	writeDomainComponent := fs.String("write-domain-component", "", "写域组件，默认同 -module")
	writePaths := fs.String("write-paths", "", "逗号分隔的仓相对写路径")
	writeResources := fs.String("write-resources", "", "逗号分隔的封闭资源 kind:id")
	engine := fs.String("engine", "", "writer/reviewer 引擎（必填，必须是 tick 能钉定且不会 fail-open 的执行器）")
	writerEngine := fs.String("writer-engine", "", "写者引擎，默认同 -engine")
	reviewerEngine := fs.String("reviewer-engine", "", "审核引擎，默认同 -engine")
	selectGoal := fs.String("select-goal", "", "目标陈述（-goal 的别名）")
	designModel := fs.String("design-model", "", "设计节点模型（只读角色；须配合 -design-receipt 或 -design-task）")
	designRunner := fs.String("design-runner", "", "设计节点 runner（只读角色；须配合收据或已完成设计任务）")
	designActualModel := fs.String("design-actual-model", "", "设计节点实际模型")
	designActualRunner := fs.String("design-actual-runner", "", "设计节点实际 runner")
	designIdentity := fs.String("design-identity", "", "独立设计身份（Astra/Fable）")
	designReceipt := fs.String("design-receipt", "", "已完成独立设计的外部收据 JSON")
	designTask := fs.String("design-task", "", "已完成的独立只读设计 Task ID")
	if err := parseWorkflowFlags(fs, args); err != nil {
		return err
	}

	root := resolveRoot(*rootFlag)
	cfg, err := loadConfig(root)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*moduleID) == "" {
		return fmt.Errorf("-module 不能为空")
	}
	if strings.TrimSpace(*goal) == "" {
		*goal = strings.TrimSpace(*selectGoal)
	}
	if strings.TrimSpace(*goal) == "" {
		return fmt.Errorf("-goal 不能为空")
	}
	if strings.TrimSpace(*criteria) == "" {
		return fmt.Errorf("-terminal-criteria 不能为空")
	}
	wd, err := resolveDir(*dir)
	if err != nil {
		return err
	}
	repoRoot := strings.TrimSpace(*repo)
	if repoRoot == "" {
		if top, _, unc := resolveGitIdentity(wd); !unc && top != "" {
			repoRoot = top
		} else {
			repoRoot = wd
		}
	} else if repoRoot, err = filepath.Abs(repoRoot); err != nil {
		return err
	}

	wEngine, rEngine := strings.TrimSpace(*writerEngine), strings.TrimSpace(*reviewerEngine)
	if wEngine == "" {
		wEngine = strings.TrimSpace(*engine)
	}
	if rEngine == "" {
		rEngine = strings.TrimSpace(*engine)
	}
	if wEngine == "" || rEngine == "" {
		return fmt.Errorf("%w: 必须显式指定 -engine（或 -writer-engine/-reviewer-engine）", errWorkflowUnknownEngine)
	}

	domain := WriteDomain{
		ID:        firstNonBlank(*writeDomainID, *moduleID),
		Lineage:   firstNonBlank(*writeDomainLineage, *moduleID+"-lineage"),
		Component: firstNonBlank(*writeDomainComponent, *moduleID),
		Paths:     splitComma(*writePaths),
	}
	if domain.Resources, err = parseResourceClaims(*writeResources); err != nil {
		return err
	}

	id, err := newWorkflowID(root)
	if err != nil {
		return err
	}
	wf := &WorkflowRecord{
		Schema:           workflowSchemaV1,
		ID:               id,
		Mode:             strings.TrimSpace(*mode),
		ModuleID:         strings.TrimSpace(*moduleID),
		GoalID:           firstNonBlank(*goalID, *moduleID),
		Goal:             strings.TrimSpace(*goal),
		ParentID:         strings.TrimSpace(*parent),
		Repo:             repoRoot,
		Worktree:         wd,
		WriteDomain:      domain,
		TerminalCriteria: strings.TrimSpace(*criteria),
		MaxRounds:        *maxRounds,
		WriterEngine:     wEngine,
		ReviewerEngine:   rEngine,
		EffectGates: WorkflowEffectGates{
			Integration: effectGateHeld,
			Live:        effectGateHeld,
			Cutover:     effectGateHeld,
		},
		Status: workflowStatusDesign,
	}
	if err := bindInitialDesignProof(wf, designBindRequest{
		Model: *designModel, Runner: *designRunner,
		ActualModel: *designActualModel, ActualRunner: *designActualRunner,
		Identity: *designIdentity, ReceiptPath: *designReceipt,
		DesignTaskID: *designTask, Root: root,
	}); err != nil {
		return err
	}
	if err := normalizeWorkflowRecord(cfg, wf); err != nil {
		return err
	}
	if wf.Mode == workflowModeFederated && wf.ParentID != "" {
		if _, err := loadWorkflow(root, cfg, wf.ParentID); err != nil {
			return fmt.Errorf("%w: parent %s not loadable: %v", errWorkflowParent, wf.ParentID, err)
		}
	}
	if err := auditWorkflowWriteDomains(root, cfg, wf); err != nil {
		return err
	}
	if _, err := createHeldIntegrationTask(root, cfg, wf); err != nil {
		return err
	}
	if err := persistWorkflow(root, cfg, wf); err != nil {
		return err
	}
	fmt.Printf("已创建 workflow %s mode=%s module=%s integration=%s（held）\n",
		wf.ID, wf.Mode, wf.ModuleID, wf.IntegrationTaskID)
	return nil
}

func cmdWorkflowList(args []string) error {
	fs := flag.NewFlagSet("workflow list", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	asJSON := fs.Bool("json", false, "输出 JSON")
	if err := parseWorkflowFlags(fs, args); err != nil {
		return err
	}
	root := resolveRoot(*rootFlag)
	cfg, err := loadConfig(root)
	if err != nil {
		return err
	}
	wfs, err := loadWorkflows(root, cfg)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(wfs)
	}
	if len(wfs) == 0 {
		fmt.Println("没有 workflow。用 cardex workflow init 创建。")
		return nil
	}
	for _, wf := range wfs {
		fmt.Printf("%s\t%s\t%s\t%s\tround %d/%d\tintegration=%s\tlive=%s\n",
			wf.ID, wf.Mode, wf.ModuleID, wf.Status, wf.CurrentRound, wf.MaxRounds,
			wf.EffectGates.Integration, wf.EffectGates.Live)
	}
	return nil
}

func cmdWorkflowShow(args []string) error {
	fs := flag.NewFlagSet("workflow show", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	if err := parseWorkflowFlags(fs, args); err != nil {
		return err
	}
	root, _, wf, err := workflowTarget(fs, rootFlag, "cardex workflow show <id>")
	if err != nil {
		return err
	}
	return encodeWorkflowShow(os.Stdout, root, wf)
}

func cmdWorkflowWriter(args []string) error {
	fs := flag.NewFlagSet("workflow writer", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	prompt := fs.String("prompt", "", "覆盖写者 prompt（默认用 workflow-writer 模板）")
	mode := fs.String("mode", "", "native 或 manual；空=普通 writer（tick 可派发）")
	if err := parseWorkflowFlags(fs, args); err != nil {
		return err
	}
	root, cfg, wf, err := workflowTarget(fs, rootFlag, "cardex workflow writer <id> [-mode native|manual] [-prompt ...]")
	if err != nil {
		return err
	}
	t, err := admitWorkflowWriterMode(root, cfg, wf, *prompt, *mode)
	if err != nil {
		return err
	}
	modeNote := *mode
	if modeNote == "" {
		modeNote = "ordinary"
	}
	fmt.Printf("workflow %s writer=%s round=%d engine=%s mode=%s\n", wf.ID, t.ID, wf.CurrentRound, t.PreferRunner, modeNote)
	return nil
}

func cmdWorkflowGoalRun(args []string) error {
	fs := flag.NewFlagSet("workflow goal-run", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	manual := fs.Bool("manual", false, "前台启动交互式 Grok /goal（必填；-p 不是 goal 证明）")
	budget := fs.Int64("budget", 0, "软 token 预算，写入 /goal --budget")
	if err := parseWorkflowFlags(fs, args); err != nil {
		return err
	}
	if !*manual {
		return fmt.Errorf("%w", errGoalManualRequired)
	}
	root, cfg, wf, err := workflowTarget(fs, rootFlag, "cardex workflow goal-run <id> -manual [-budget N]")
	if err != nil {
		return err
	}
	return launchManualWorkflowGoal(root, cfg, wf, *budget)
}

func cmdWorkflowGoalSync(args []string) error {
	fs := flag.NewFlagSet("workflow goal-sync", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	if err := parseWorkflowFlags(fs, args); err != nil {
		return err
	}
	root, cfg, wf, err := workflowTarget(fs, rootFlag, "cardex workflow goal-sync <id>")
	if err != nil {
		return err
	}
	t, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{})
	if err != nil {
		return err
	}
	obs, sess, goalID, attempt := "", "", "", ""
	if t != nil {
		sess, attempt = t.SessionID, t.ActiveAttemptID
		if t.Goal != nil {
			obs = t.Goal.Observation
			goalID = t.Goal.NativeGoalID
			if attempt == "" {
				attempt = t.Goal.BoundAttemptID
			}
		}
		fmt.Printf("workflow %s task=%s status=%s session=%s goal_id=%s attempt=%s observation=%s\n",
			wf.ID, t.ID, t.Status, orDash(sess), orDash(goalID), orDash(attempt), orDash(obs))
	}
	return nil
}

func cmdWorkflowDesignResult(args []string) error {
	fs := flag.NewFlagSet("workflow design-result", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	decision := fs.String("decision", "", "stop、input、successor、accept 或 revise")
	observation := fs.String("observation", "", "failed、paused、needs-input、budget_limited 或 complete")
	receipt := fs.String("design-receipt", "", "消费当前阶段的新鲜独立设计结果")
	if err := parseWorkflowFlags(fs, args); err != nil {
		return err
	}
	root, cfg, wf, err := workflowTarget(fs, rootFlag,
		"cardex workflow design-result <id> -design-receipt R -decision stop|input|successor|accept|revise [-observation failed|paused|needs-input|budget_limited|complete]")
	if err != nil {
		return err
	}
	t, err := applyWorkflowDesignResult(root, cfg, wf, *decision, *observation, *receipt)
	if err != nil {
		return err
	}
	id := ""
	if t != nil {
		id = t.ID
	}
	fmt.Printf("workflow %s design-result=%s observation=%s task=%s status=%s\n",
		wf.ID, *decision, *observation, orDash(id), wf.Status)
	return nil
}

func cmdWorkflowDesignRepair(args []string) error {
	fs := flag.NewFlagSet("workflow design-repair", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	receipt := fs.String("design-receipt", "", "消费当前候选的新鲜独立设计结果")
	if err := parseWorkflowFlags(fs, args); err != nil {
		return err
	}
	root, cfg, wf, err := workflowTarget(fs, rootFlag,
		"cardex workflow design-repair <id> -design-receipt R")
	if err != nil {
		return err
	}
	if err := bindWorkflowDesignRepair(root, cfg, wf, *receipt); err != nil {
		return err
	}
	latest := wf.DesignLineage.LatestValid
	fmt.Printf("workflow %s design-repair=%d latest_model=%s latest_runner=%s\n",
		wf.ID, wf.DesignLineage.RepairCount,
		firstNonBlank(latest.ActualModel, latest.Model),
		firstNonBlank(latest.ActualRunner, latest.Runner))
	return nil
}

func cmdWorkflowFreeze(args []string) error {
	fs := flag.NewFlagSet("workflow freeze-candidate", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	commit := fs.String("commit", "", "候选 commit")
	tree := fs.String("tree", "", "候选 tree")
	branch := fs.String("branch", "", "候选分支")
	paths := fs.String("changed-paths", "", "逗号分隔 changed paths")
	if err := parseWorkflowFlags(fs, args); err != nil {
		return err
	}
	root, cfg, wf, err := workflowTarget(fs, rootFlag,
		"cardex workflow freeze-candidate <id> -commit C -tree T")
	if err != nil {
		return err
	}
	cand := WorkflowCandidate{
		Commit:       *commit,
		Tree:         *tree,
		Branch:       *branch,
		ChangedPaths: splitComma(*paths),
	}
	if err := freezeWorkflowCandidate(root, cfg, wf, cand); err != nil {
		return err
	}
	fmt.Printf("workflow %s 已冻结候选 commit=%s tree=%s\n", wf.ID, wf.Candidate.Commit, wf.Candidate.Tree)
	return nil
}

func cmdWorkflowReview(args []string) error {
	fs := flag.NewFlagSet("workflow review", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	if err := parseWorkflowFlags(fs, args); err != nil {
		return err
	}
	root, cfg, wf, err := workflowTarget(fs, rootFlag, "cardex workflow review <id>")
	if err != nil {
		return err
	}
	t, err := admitWorkflowReviewer(root, cfg, wf)
	if err != nil {
		return err
	}
	fmt.Printf("workflow %s reviewer=%s review_of=%s engine=%s\n", wf.ID, t.ID, t.ReviewOf, t.PreferRunner)
	return nil
}

func cmdWorkflowIngest(args []string) error {
	fs := flag.NewFlagSet("workflow ingest-review", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	if err := parseWorkflowFlags(fs, args); err != nil {
		return err
	}
	root, cfg, wf, err := workflowTarget(fs, rootFlag, "cardex workflow ingest-review <id>")
	if err != nil {
		return err
	}
	if err := ingestWorkflowReview(root, cfg, wf); err != nil {
		return err
	}
	verdict, hold := "", ""
	if wf.Review != nil {
		verdict, hold = wf.Review.Verdict, wf.Review.HoldReason
	}
	fmt.Printf("workflow %s status=%s verdict=%s hold=%s integration=%s\n",
		wf.ID, wf.Status, orDash(verdict), orDash(hold), wf.EffectGates.Integration)
	return nil
}

func cmdWorkflowRepair(args []string) error {
	fs := flag.NewFlagSet("workflow repair", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	summary := fs.String("summary", "", "修复轮摘要")
	if err := parseWorkflowFlags(fs, args); err != nil {
		return err
	}
	root, cfg, wf, err := workflowTarget(fs, rootFlag, "cardex workflow repair <id> [-summary ...]")
	if err != nil {
		return err
	}
	findings := ""
	if wf.Review != nil {
		for i, p := range wf.Review.P0 {
			findings += fmt.Sprintf("P0-%d: %s\n", i+1, p)
		}
		for i, p := range wf.Review.P1 {
			findings += fmt.Sprintf("P1-%d: %s\n", i+1, p)
		}
	}
	t, err := admitWorkflowRepair(root, cfg, wf, findings, *summary)
	if err != nil {
		return err
	}
	fmt.Printf("workflow %s repair=%s round=%d/%d\n", wf.ID, t.ID, wf.CurrentRound, wf.MaxRounds)
	return nil
}

func cmdWorkflowTryRelease(args []string) error {
	fs := flag.NewFlagSet("workflow try-release-integration", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	if err := parseWorkflowFlags(fs, args); err != nil {
		return err
	}
	root, cfg, wf, err := workflowTarget(fs, rootFlag, "cardex workflow try-release-integration <id>")
	if err != nil {
		return err
	}
	if err := tryReleaseWorkflowIntegration(root, cfg, wf); err != nil {
		return err
	}
	fmt.Printf("workflow %s integration=%s（live=%s cutover=%s 仍 held）\n",
		wf.ID, wf.EffectGates.Integration, wf.EffectGates.Live, wf.EffectGates.Cutover)
	return nil
}

func cmdWorkflowMark(args []string) error {
	fs := flag.NewFlagSet("workflow mark", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	kind := fs.String("kind", "", "external | owner | exhausted")
	summary := fs.String("summary", "", "简要原因")
	if err := parseWorkflowFlags(fs, args); err != nil {
		return err
	}
	root, cfg, wf, err := workflowTarget(fs, rootFlag,
		"cardex workflow mark <id> -kind external|owner|exhausted -summary ...")
	if err != nil {
		return err
	}
	if err := markWorkflowRoute(root, cfg, wf, *kind, *summary); err != nil {
		return err
	}
	fmt.Printf("workflow %s status=%s（已写 Root 通知 %s）\n", wf.ID, wf.Status, wf.MaterialNotify.Kind)
	return nil
}
