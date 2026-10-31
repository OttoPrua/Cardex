package model

import (
	"encoding/json"
	"time"
)

// Project is one repository Cardex drives.
type Project struct {
	ID             string   `json:"id"`              // 项目标识。
	RepoPath       string   `json:"repo_path"`       // 主 checkout 路径。
	WorktreeRoot   string   `json:"worktree_root"`   // worktree 根目录，默认在项目根的 worktrees/cardex/。
	TestCmd        string   `json:"test_cmd"`        // 默认的全量测试命令。
	DesignDir      string   `json:"design_dir"`      // 设计文档目录，默认 docs/design/。
	ForbiddenPaths []string `json:"forbidden_paths"` // 触碰前需要 Otto 确认的路径。
}

// Acceptance is an objective's commands and checklist. Both are optional.
type Acceptance struct {
	Commands  []string `json:"commands"`  // 验收命令。
	Checklist []string `json:"checklist"` // 验收检查清单。
}

// Budget is a soft reminder line and a hard stop, in tokens.
type Budget struct {
	SoftTokens int64 `json:"soft_tokens"` // 软预算，用到八成时提醒。
	HardTokens int64 `json:"hard_tokens"` // 硬预算，达到后暂停。
}

// Objective is a body of work inside a project.
type Objective struct {
	ID                string          `json:"id"`                 // 目标标识。
	Project           string          `json:"project"`            // 所属项目。
	Title             string          `json:"title"`              // 目标标题。
	Acceptance        Acceptance      `json:"acceptance"`         // 验收命令和检查清单。
	Budget            Budget          `json:"budget"`             // 目标的软硬 token 预算。
	Priority          int             `json:"priority"`           // 调度优先级，数值越大越优先。
	Mode              ObjectiveMode   `json:"mode"`               // auto 自动生成卡，manual 不自动生成。
	FreezeMode        FreezeMode      `json:"freeze_mode"`        // safe 或 fast。
	Origin            string          `json:"origin"`             // 创建者会话，形如 kind:id。
	Status            ObjectiveStatus `json:"status"`             // active、paused、done_candidate、done、canceled。
	IntegrationBranch string          `json:"integration_branch"` // 集成分支，cardex/<objective>/main。
	Channel           string          `json:"channel"`            // 显式指定的渠道；空则自动路由，且不受红线过滤。
	Model             string          `json:"model"`              // 显式指定的模型；空则自动路由。
}

// DesignNode is one node in an objective's design tree.
type DesignNode struct {
	ID            string           `json:"id"`             // 节点标识。
	Objective     string           `json:"objective"`      // 所属目标。
	Parent        string           `json:"parent"`         // 父节点；方向层为空。
	Layer         Layer            `json:"layer"`          // direction、module 或 detail。
	DocPath       string           `json:"doc_path"`       // 集成分支上的节点文档路径。
	OpenQuestions OpenQuestionList `json:"open_questions"` // 尚未关闭的问题。旧数据可以是字符串数组。
	Status        NodeStatus       `json:"status"`         // drafting、freezable、frozen、reopened。
}

// OpenQuestion is one design-node open question. Design §3 and §6.6.
// It is not a Question（待拍板）.
type OpenQuestion struct {
	ID         string             `json:"id"`          // 问题标识。
	Text       string             `json:"text"`        // 问题原文。旧字符串条目只填这一项。
	Type       OpenQuestionType   `json:"type"`        // fact、experiment、decision 或 otto。
	Status     OpenQuestionStatus `json:"status"`      // open、researching、answered、stale 或 unanswered。
	Asks       int                `json:"asks"`        // 复查次数。
	Answer     string             `json:"answer"`      // 结论。
	Evidence   string             `json:"evidence"`    // 证据：文件行号、命令输出或 URL。
	Confidence string             `json:"confidence"`  // 把握：高、中、低。
	Commit     string             `json:"commit"`      // 派发时集成分支 HEAD。
	CitedPaths []string           `json:"cited_paths"` // 证据里的路径。
}

// Legacy is the v1 status and source kept on an imported card.
type Legacy struct {
	Status     string `json:"status"`      // v1 原始状态。
	SourcePath string `json:"source_path"` // v1 来源路径。
	Reason     string `json:"reason"`      // v1 挂起或失败原因。
}

// Claim is a path and an optional symbol a card says it will change.
type Claim struct {
	ID     string `json:"id"`     // 声明标识。
	Card   string `json:"card"`   // 所属卡。
	Path   string `json:"path"`   // 路径；目录声明只表示在该目录下新建文件。
	Symbol string `json:"symbol"` // 可选符号：顶层函数、Type.Method 或类型；空表示整文件。
}

// Card is one unit of scheduled work.
type Card struct {
	ID           string     `json:"id"`            // 卡标识。
	Objective    string     `json:"objective"`     // 所属目标。
	Node         string     `json:"node"`          // 所属设计节点。
	Kind         CardKind   `json:"kind"`          // 卡的种类。
	Title        string     `json:"title"`         // 卡标题。
	Body         string     `json:"body"`          // 卡正文。
	Tier         Tier       `json:"tier"`          // T1、T2 或 T3；空则入队时分级。
	Requires     []string   `json:"requires"`      // 能力要求，例如 web、goal_mode。
	Claims       []Claim    `json:"claims"`        // 声明的路径和符号。
	Deps         []string   `json:"deps"`          // 依赖的卡标识。
	Acceptance   []string   `json:"acceptance"`    // 验收命令。
	Base         string     `json:"base"`          // 起点；新卡默认集成分支 HEAD，续做时是本卡分支。
	Status       CardStatus `json:"status"`        // 卡状态。
	WaitReason   string     `json:"wait_reason"`   // waiting 的原因，写明被哪张卡占了哪个符号。
	ReworkCount  int        `json:"rework_count"`  // 返工次数，唯一的返工计数。
	Legacy       Legacy     `json:"legacy"`        // 从 v1 导入的原始状态和来源路径。
	Channel      string     `json:"channel"`       // 显式指定的渠道，不过滤红线。
	Model        string     `json:"model"`         // 显式指定的模型。
	ReworkNote   string     `json:"rework_note"`   // 复核意见，附进下一次尝试的 prompt。
	AuthorFamily string     `json:"author_family"` // 作者模型族；复核卡必须换族。
	Budget       Budget     `json:"budget"`        // 单卡预算。
}

// Route is the channel and model chosen before an attempt starts.
type Route struct {
	Channel string `json:"channel"` // 渠道。
	Model   string `json:"model"`   // 模型。
}

// RouteCandidate is one model considered during routing.
type RouteCandidate struct {
	Channel    string  `json:"channel"`    // 候选渠道。
	Model      string  `json:"model"`      // 候选模型。
	Family     string  `json:"family"`     // 模型族。
	Tier       Tier    `json:"tier"`       // 候选所用等级。
	Preference int     `json:"preference"` // 定位表偏好分组。
	Remaining  float64 `json:"remaining"`  // 选择时的剩余比例。
}

// FilteredCandidate records why a candidate was removed.
type FilteredCandidate struct {
	Channel string `json:"channel"` // 被过滤的渠道。
	Model   string `json:"model"`   // 被过滤的模型。
	Reason  string `json:"reason"`  // 过滤原因。
}

// RouteScore is the Pace score of a candidate that survived filtering.
type RouteScore struct {
	Channel string  `json:"channel"` // 打分的渠道。
	Model   string  `json:"model"`   // 打分的模型。
	Score   float64 `json:"score"`   // Pace 分数，剩余比例除以距重置的小时数。
}

// RouteReason is the structured explanation stored on an attempt.
type RouteReason struct {
	Candidates []RouteCandidate    `json:"candidates"` // 候选列表。
	Filtered   []FilteredCandidate `json:"filtered"`   // 被过滤的候选和原因。
	Scores     []RouteScore        `json:"scores"`     // 最终分数。
}

// Usage is token consumption with where the number came from.
type Usage struct {
	Tokens     int64   `json:"tokens"`     // 消耗的 token 数。
	Source     string  `json:"source"`     // 用量来源，例如事件流或时长估算。
	Confidence float64 `json:"confidence"` // 置信度，0 到 1。
}

// Attempt is one executor run of a card.
type Attempt struct {
	ID           string      `json:"id"`            // 尝试标识。
	Card         string      `json:"card"`          // 所属卡。
	Index        int         `json:"index"`         // 同一张卡上的尝试序号，从 0 起。
	Route        Route       `json:"route"`         // 派发前确定的渠道和模型。
	RouteReason  RouteReason `json:"route_reason"`  // 选择理由。
	Worktree     string      `json:"worktree"`      // 本次尝试的 worktree 路径。
	Branch       string      `json:"branch"`        // 卡分支，cardex/<objective>/c/<card>。
	BaseCommit   string      `json:"base_commit"`   // 起点提交。
	HeadCommit   string      `json:"head_commit"`   // 结束时的提交。
	PID          int         `json:"pid"`           // 宿主进程号，供重启后对账。
	StartedAt    time.Time   `json:"started_at"`    // 开始时间。
	EndedAt      time.Time   `json:"ended_at"`      // 结束时间。
	Exit         *int        `json:"exit"`          // 执行器退出码；未结束为空。
	Usage        Usage       `json:"usage"`         // token 用量及其来源和置信度。
	Outcome      Outcome     `json:"outcome"`       // 收割得出的结果。
	ErrorClass   ErrorClass  `json:"error_class"`   // 执行器错误类。
	ErrorExcerpt string      `json:"error_excerpt"` // 错误摘录。
	PromptPath   string      `json:"prompt_path"`   // 调度方事先写好的 prompt 文件路径。
}

// QuotaWindow is one configured usage window on a channel.
type QuotaWindow struct {
	Name          string    `json:"name"`           // 窗口名，例如 5h、daily、weekly、monthly。
	ResetRule     string    `json:"reset_rule"`     // 重置规则：固定时刻或滚动。
	ResetAt       time.Time `json:"reset_at"`       // 本窗口的重置时间。
	LimitTokens   int64     `json:"limit_tokens"`   // token 上限；0 表示未配置。
	LimitRequests int64     `json:"limit_requests"` // 请求上限；0 表示未配置。
	UsedTokens    int64     `json:"used_tokens"`    // 窗口内已计入的 token。
	Remaining     float64   `json:"remaining"`      // 剩余比例，1 表示全满；未配置上限时无意义。
	Confidence    float64   `json:"confidence"`     // 余量置信度。
}

// Channel is one configured executor account.
type Channel struct {
	ID            string        `json:"id"`             // 渠道标识。
	Runner        RunnerName    `json:"runner"`         // 执行器种类。
	EngineProfile string        `json:"engine_profile"` // 仅 claude 使用的 engines 档案。
	SourceGroup   string        `json:"source_group"`   // 同一订阅或账号。
	Family        string        `json:"family"`         // 模型族，复核卡要求与作者不同。
	Tier          Tier          `json:"tier"`           // 注册时声明的等级，供调度和测试使用。
	Models        []string      `json:"models"`         // 可用模型。
	Concurrency   int           `json:"concurrency"`    // 并发上限。
	QuotaWindows  []QuotaWindow `json:"quota_windows"`  // 额度窗口。
	Redline       float64       `json:"redline"`        // 保留比例，剩余比例小于等于它则不参与自动选择。
	Health        Health        `json:"health"`         // ok、auth_failed 或 degraded。
	LastError     string        `json:"last_error"`     // 最近一次错误。
}

// Positioning is one approved row of the model-positioning table.
type Positioning struct {
	ID           string   `json:"id"`           // 定位行标识。
	Version      int      `json:"version"`      // 定位表版本，只在提案批准后增加。
	Model        string   `json:"model"`        // 模型名。
	Family       string   `json:"family"`       // 模型族。
	Tiers        []Tier   `json:"tiers"`        // 该模型能承担的等级。
	Capabilities []string `json:"capabilities"` // 能力，例如 web、goal_mode。
	Preference   int      `json:"preference"`   // 偏好分组，路由时先按它分组。
}

// Delivery is one notification attempt for a question.
type Delivery struct {
	At          time.Time `json:"at"`          // 投递时间。
	Adapter     string    `json:"adapter"`     // 适配器：codex、hermes、lark 或 file。
	Destination string    `json:"destination"` // 投递目的地。
}

// Question is a decision waiting on a person.
type Question struct {
	ID         string       `json:"id"`         // 问题标识。
	Objective  string       `json:"objective"`  // 所属目标。
	Scope      string       `json:"scope"`      // 范围：节点或卡的标识。
	ScopeKind  string       `json:"scope_kind"` // node 或 card。
	Kind       QuestionKind `json:"kind"`       // freeze、reopen、breaker、budget、refusal、done、custom。
	Text       string       `json:"text"`       // 问题正文。
	Options    []string     `json:"options"`    // 可选答案。
	Answer     string       `json:"answer"`     // 已选答案；空表示未回复。
	Deliveries []Delivery   `json:"deliveries"` // 投递记录。
}

// Event is an append-only fact used by the board and the scorecard.
type Event struct {
	ID     string          `json:"id"`     // 事件标识。
	TS     time.Time       `json:"ts"`     // 发生时间。
	Entity string          `json:"entity"` // 相关实体，形如 card:<id>。
	Type   string          `json:"type"`   // 事件类型。
	Data   json.RawMessage `json:"data"`   // 事件数据。
}
