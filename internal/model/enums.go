package model

import "strings"

// CardKind is the closed set of card kinds in design §6.3.
type CardKind string

const (
	KindDesign    CardKind = "design"    // 推进一个设计节点。
	KindResearch  CardKind = "research"  // 回答 fact 问题，附证据，不合并。
	KindSpike     CardKind = "spike"     // 实做验证，不合并。
	KindPlan      CardKind = "plan"      // 为已冻结节点生成卡清单。
	KindSkeleton  CardKind = "skeleton"  // 定接口和桩。
	KindDev       CardKind = "dev"       // 实现。
	KindReview    CardKind = "review"    // 复核开发卡。
	KindRepair    CardKind = "repair"    // 修测试或按复核意见返工。
	KindRebase    CardKind = "rebase"    // 解决合并冲突。
	KindReplan    CardKind = "replan"    // 调整尚未开始的卡。
	KindNormalize CardKind = "normalize" // 从混乱结果中提取结构。
	KindRanking   CardKind = "ranking"   // 模型定位提案。
	KindBacklog   CardKind = "backlog"   // 低风险积压，供余量填充。
)

// Tier is T1, T2, or T3. Empty means enqueue classification fills it in.
type Tier string

const (
	TierNone Tier = ""   // 尚未分级。
	TierT1   Tier = "T1" // 低风险或整理。
	TierT2   Tier = "T2" // 普通实现。
	TierT3   Tier = "T3" // 设计、复核或高难度。
)

// Outcome is the harvest result. Values are the design §5 table.
type Outcome string

const (
	OutcomePassed           Outcome = "passed"            // 有改动且验收通过，或不产出代码的卡结果有效。
	OutcomeTestsFailed      Outcome = "tests_failed"      // 验收或测试失败。
	OutcomeNoOutput         Outcome = "no_output"         // 没有改动，也没有有效结果。
	OutcomeResultUnparsable Outcome = "result_unparsable" // 结果文件无法解析。
	OutcomeQuotaHit         Outcome = "quota_hit"         // 撞到额度。
	OutcomeAuthFailed       Outcome = "auth_failed"       // 认证失败。
	OutcomeTransport        Outcome = "transport"         // 传输失败。
	OutcomeTimeout          Outcome = "timeout"           // 超时。
	OutcomePermissionDenied Outcome = "permission_denied" // 执行器权限被拒绝。
	OutcomeRefusal          Outcome = "refusal"           // 模型拒答。
	OutcomeBudgetLimited    Outcome = "budget_limited"    // 单卡预算用尽。
	OutcomeViolation        Outcome = "violation"         // 触碰硬底线。
	OutcomeLaunchFailed     Outcome = "launch_failed"     // 建 worktree、拼 prompt 或启动宿主失败。
)

// ErrorClass is the runner classification. Values are the design §5 table.
type ErrorClass string

const (
	ErrorNone             ErrorClass = ""                  // 尚未分类。
	ErrorPassed           ErrorClass = "passed"            // 执行器侧看到通过。
	ErrorTestsFailed      ErrorClass = "tests_failed"      // 执行器侧看到测试失败。
	ErrorNoOutput         ErrorClass = "no_output"         // 执行器侧看到没有产出。
	ErrorResultUnparsable ErrorClass = "result_unparsable" // 执行器侧看到结果无法解析。
	ErrorQuotaHit         ErrorClass = "quota_hit"         // 撞到额度，可能附带重置时间。
	ErrorAuthFailed       ErrorClass = "auth_failed"       // 认证失败。
	ErrorTransport        ErrorClass = "transport"         // 传输失败。
	ErrorTimeout          ErrorClass = "timeout"           // 超时。
	ErrorPermissionDenied ErrorClass = "permission_denied" // 权限被拒绝。
	ErrorRefusal          ErrorClass = "refusal"           // 拒答。
	ErrorBudgetLimited    ErrorClass = "budget_limited"    // 预算用尽。
	ErrorViolation        ErrorClass = "violation"         // 触碰硬底线。
	ErrorLaunchFailed     ErrorClass = "launch_failed"     // 启动失败，与 OutcomeLaunchFailed 同一取值。
)

// FreezeMode decides who confirms a freeze. Design §6.2.
type FreezeMode string

const (
	FreezeSafe FreezeMode = "safe" // 方向和模块层需要 Otto 确认。
	FreezeFast FreezeMode = "fast" // 模块和细节层自动冻结。
)

// Layer is a design-tree level. Design §6.2.
type Layer string

const (
	LayerDirection Layer = "direction" // 目标方向。
	LayerModule    Layer = "module"    // 模块划分。
	LayerDetail    Layer = "detail"    // 模块内细节。
)

// ObjectiveMode selects automatic card generation. Design §6.1.
type ObjectiveMode string

const (
	ModeAuto   ObjectiveMode = "auto"   // 自动推进设计、拆卡和重规划。
	ModeManual ObjectiveMode = "manual" // 不自动生成卡，派发和合并照常。
)

// ObjectiveStatus is the lifecycle of an objective. Design §3.
type ObjectiveStatus string

const (
	ObjectiveActive        ObjectiveStatus = "active"         // 进行中。
	ObjectivePaused        ObjectiveStatus = "paused"         // 暂停。
	ObjectiveDoneCandidate ObjectiveStatus = "done_candidate" // 等待确认完成。
	ObjectiveDone          ObjectiveStatus = "done"           // 已完成。
	ObjectiveCanceled      ObjectiveStatus = "canceled"       // 已取消。
)

// NodeStatus is the lifecycle of a design node. Design §3.
type NodeStatus string

const (
	NodeDrafting  NodeStatus = "drafting"  // 起草中。
	NodeFreezable NodeStatus = "freezable" // 可以冻结。
	NodeFrozen    NodeStatus = "frozen"    // 已冻结。
	NodeReopened  NodeStatus = "reopened"  // 已重开。
)

// CardStatus is the lifecycle of a card. Design §3. There is no held state.
type CardStatus string

const (
	CardReady       CardStatus = "ready"        // 可以派发。
	CardWaiting     CardStatus = "waiting"      // 被依赖、声明冲突或问题挡住。
	CardRunning     CardStatus = "running"      // 尝试进行中。
	CardHarvesting  CardStatus = "harvesting"   // 正在收割。
	CardReviewing   CardStatus = "reviewing"    // 正在复核。
	CardMergeQueued CardStatus = "merge_queued" // 已进入合并队列。
	CardMerged      CardStatus = "merged"       // 已合入集成分支。
	CardDone        CardStatus = "done"         // 不产出代码的卡已完成。
	CardNeedsInput  CardStatus = "needs_input"  // 待复核或待拍板。
	CardPaused      CardStatus = "paused"       // 暂停。
	CardCanceled    CardStatus = "canceled"     // 已取消。
	CardFailed      CardStatus = "failed"       // 失败。
)

// Health is a channel's usability. Design §3.
type Health string

const (
	HealthOK         Health = "ok"          // 可以派发。
	HealthAuthFailed Health = "auth_failed" // 认证失败，不可自动选择。
	HealthDegraded   Health = "degraded"    // 退化，不可自动选择。
)

// RunnerName is the executor family configured on a channel. Design §3.
type RunnerName string

const (
	RunnerGrok     RunnerName = "grok"     // grok 执行器。
	RunnerCodex    RunnerName = "codex"    // codex 执行器。
	RunnerClaude   RunnerName = "claude"   // claude 执行器。
	RunnerCursor   RunnerName = "cursor"   // cursor 执行器。
	RunnerKimi     RunnerName = "kimi"     // kimi 执行器。
	RunnerAGY      RunnerName = "agy"      // agy 执行器。
	RunnerOpenCode RunnerName = "opencode" // opencode 执行器。
	RunnerFake     RunnerName = "fake"     // e2e 测试执行器，不是 §3 生产执行器。
)

// QuestionKind is why a human decision was requested. Design §3.
type QuestionKind string

const (
	QuestionFreeze  QuestionKind = "freeze"  // 冻结确认。
	QuestionReopen  QuestionKind = "reopen"  // 重开设计节点。
	QuestionBreaker QuestionKind = "breaker" // 节点熔断。
	QuestionBudget  QuestionKind = "budget"  // 预算触顶。
	QuestionRefusal QuestionKind = "refusal" // 拒答或权限拒绝。
	QuestionDone    QuestionKind = "done"    // 目标完成确认。
	QuestionCustom  QuestionKind = "custom"  // 其他需要拍板的问题。
)

// Event types written by the scheduler. Event.Type is otherwise a free string.
const (
	EventLaunchFailed = "launch_failed" // 启动失败。
	EventTickError    = "tick_error"    // 一轮中某一步出错。
)

// AllCardKinds returns every §6.3 kind.
func AllCardKinds() []CardKind {
	return []CardKind{
		KindDesign, KindResearch, KindSpike, KindPlan, KindSkeleton, KindDev, KindReview,
		KindRepair, KindRebase, KindReplan, KindNormalize, KindRanking, KindBacklog,
	}
}

// DefaultTier is the §6.3 kind default. Dev falls back to T2 when classification does not run.
func DefaultTier(k CardKind) Tier {
	switch k {
	case KindDesign, KindPlan, KindSkeleton, KindReview, KindReplan:
		return TierT3
	case KindSpike, KindDev, KindRepair, KindRebase, KindRanking:
		return TierT2
	case KindResearch, KindNormalize, KindBacklog:
		return TierT1
	default:
		return TierT2
	}
}

// OpenQuestionType is the §6.6 label on a design-node open question.
type OpenQuestionType string

const (
	OQFact       OpenQuestionType = "fact"       // 查一下就知道。
	OQExperiment OpenQuestionType = "experiment" // 做了才知道。
	OQDecision   OpenQuestionType = "decision"   // 需要权衡。无标签的旧条目用这个。
	OQOtto       OpenQuestionType = "otto"       // 需要 Otto 决定。
)

// OpenQuestionStatus is the lifecycle of a design-node open question.
type OpenQuestionStatus string

const (
	OQOpen        OpenQuestionStatus = "open"        // 尚未派研究。
	OQResearching OpenQuestionStatus = "researching" // 研究卡正在查。
	OQAnswered    OpenQuestionStatus = "answered"    // 已有结论。
	OQStale       OpenQuestionStatus = "stale"       // 引用路径在 commit 之后有改动。
	OQUnanswered  OpenQuestionStatus = "unanswered"  // 研究卡没有作答。
)

// AllOpenQuestionTypes returns fact, experiment, decision, and otto.
func AllOpenQuestionTypes() []OpenQuestionType {
	return []OpenQuestionType{OQFact, OQExperiment, OQDecision, OQOtto}
}

// AllOpenQuestionStatuses returns every open-question status.
func AllOpenQuestionStatuses() []OpenQuestionStatus {
	return []OpenQuestionStatus{OQOpen, OQResearching, OQAnswered, OQStale, OQUnanswered}
}

// AllTiers returns the assignable tiers. Empty is valid and is not included.
func AllTiers() []Tier { return []Tier{TierT1, TierT2, TierT3} }

// AllOutcomes returns every design §5 outcome.
func AllOutcomes() []Outcome {
	return []Outcome{
		OutcomePassed, OutcomeTestsFailed, OutcomeNoOutput, OutcomeResultUnparsable,
		OutcomeQuotaHit, OutcomeAuthFailed, OutcomeTransport, OutcomeTimeout,
		OutcomePermissionDenied, OutcomeRefusal, OutcomeBudgetLimited, OutcomeViolation,
		OutcomeLaunchFailed,
	}
}

// AllErrorClasses returns every design §5 error class.
func AllErrorClasses() []ErrorClass {
	return []ErrorClass{
		ErrorPassed, ErrorTestsFailed, ErrorNoOutput, ErrorResultUnparsable,
		ErrorQuotaHit, ErrorAuthFailed, ErrorTransport, ErrorTimeout,
		ErrorPermissionDenied, ErrorRefusal, ErrorBudgetLimited, ErrorViolation,
		ErrorLaunchFailed,
	}
}

// AllFreezeModes returns safe and fast.
func AllFreezeModes() []FreezeMode { return []FreezeMode{FreezeSafe, FreezeFast} }

// AllLayers returns the three design-tree layers.
func AllLayers() []Layer { return []Layer{LayerDirection, LayerModule, LayerDetail} }

// AllObjectiveModes returns auto and manual.
func AllObjectiveModes() []ObjectiveMode { return []ObjectiveMode{ModeAuto, ModeManual} }

// AllObjectiveStatuses returns every objective status.
func AllObjectiveStatuses() []ObjectiveStatus {
	return []ObjectiveStatus{
		ObjectiveActive, ObjectivePaused, ObjectiveDoneCandidate, ObjectiveDone, ObjectiveCanceled,
	}
}

// AllNodeStatuses returns every design-node status.
func AllNodeStatuses() []NodeStatus {
	return []NodeStatus{NodeDrafting, NodeFreezable, NodeFrozen, NodeReopened}
}

// AllCardStatuses returns every card status.
func AllCardStatuses() []CardStatus {
	return []CardStatus{
		CardReady, CardWaiting, CardRunning, CardHarvesting, CardReviewing, CardMergeQueued,
		CardMerged, CardDone, CardNeedsInput, CardPaused, CardCanceled, CardFailed,
	}
}

// AllHealth returns every channel health value.
func AllHealth() []Health { return []Health{HealthOK, HealthAuthFailed, HealthDegraded} }

// AllRunners returns every channel runner name.
func AllRunners() []RunnerName {
	return []RunnerName{RunnerGrok, RunnerCodex, RunnerClaude, RunnerCursor, RunnerKimi, RunnerAGY, RunnerOpenCode}
}

// AllQuestionKinds returns every question kind.
func AllQuestionKinds() []QuestionKind {
	return []QuestionKind{
		QuestionFreeze, QuestionReopen, QuestionBreaker, QuestionBudget,
		QuestionRefusal, QuestionDone, QuestionCustom,
	}
}

func oneOf[T ~string](v T, all []T) bool {
	for _, item := range all {
		if v == item {
			return true
		}
	}
	return false
}

// Valid reports whether k is one of the §6.3 kinds.
func (k CardKind) Valid() bool { return oneOf(k, AllCardKinds()) }

// Valid reports whether t is empty or T1–T3.
func (t Tier) Valid() bool { return t == TierNone || oneOf(t, AllTiers()) }

// CanonicalTier accepts a known tier that differs only by case or surrounding space.
// The bool is false when the token is empty or not T1, T2, or T3.
func CanonicalTier(raw Tier) (Tier, bool) {
	t := Tier(strings.ToUpper(strings.TrimSpace(string(raw))))
	if t == "" || !oneOf(t, AllTiers()) {
		return "", false
	}
	return t, true
}

// Valid reports whether o is empty or a §5 outcome.
func (o Outcome) Valid() bool { return o == "" || oneOf(o, AllOutcomes()) }

// Valid reports whether e is empty or a §5 error class.
func (e ErrorClass) Valid() bool { return e == "" || oneOf(e, AllErrorClasses()) }

// Valid reports whether m is safe or fast.
func (m FreezeMode) Valid() bool { return oneOf(m, AllFreezeModes()) }

// Valid reports whether l is a design-tree layer.
func (l Layer) Valid() bool { return oneOf(l, AllLayers()) }

// Valid reports whether m is auto or manual.
func (m ObjectiveMode) Valid() bool { return oneOf(m, AllObjectiveModes()) }

// Valid reports whether s is an objective status.
func (s ObjectiveStatus) Valid() bool { return oneOf(s, AllObjectiveStatuses()) }

// Valid reports whether s is a node status.
func (s NodeStatus) Valid() bool { return oneOf(s, AllNodeStatuses()) }

// Valid reports whether s is a card status.
func (s CardStatus) Valid() bool { return oneOf(s, AllCardStatuses()) }

// Valid reports whether h is a channel health value.
func (h Health) Valid() bool { return oneOf(h, AllHealth()) }

// Valid reports whether r is a §3 production runner or the harness-only fake runner.
func (r RunnerName) Valid() bool { return r == RunnerFake || oneOf(r, AllRunners()) }

// Valid reports whether k is a question kind.
func (k QuestionKind) Valid() bool { return oneOf(k, AllQuestionKinds()) }

// Valid reports whether t is fact, experiment, decision, or otto.
func (t OpenQuestionType) Valid() bool { return oneOf(t, AllOpenQuestionTypes()) }

// Valid reports whether s is an open-question status.
func (s OpenQuestionStatus) Valid() bool { return oneOf(s, AllOpenQuestionStatuses()) }
