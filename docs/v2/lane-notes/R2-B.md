# R2-B

## 做了什么
四个适配器实现 runner.Runner，Name 为 grok、cursor、kimi、agy。Build 移植 v1 写能力且沙箱内自动放行的 argv：grok 无头为 streaming-json、--sandbox workspace、--permission-mode auto、--prompt-file（值为路径）；cursor-agent 为 --print --trust --sandbox enabled，位置参数是 prompt 文件正文；kimi 为 --model --prompt（正文）stream-json，并在宿主已写好的隔离 config.toml 上只覆盖 default_permission_mode=auto 与 default_plan_mode=false，环境变量带 KIMI_CODE_NO_AUTO_UPDATE=1 与 KIMI_CODE_HOME；agy 为 --mode accept-edits、--dangerously-skip-permissions、json、--print（正文），非 thinking 模型带 --effort high。普通 grok NeedsPTY 为 false。要求 goal_mode 时 grok NeedsPTY 为 true，Drive 写入字面量 /goal 与 prompt。完成和 budget_limited 从 Grok.SessionDir 的 goal/state.json 与 updates.jsonl 读取：PTY Read 阻塞时按 100ms 轮询该目录，进程 EOF 后再读一次。交互额度终态是 grok TUI 速率卡 You've hit the rate limit for your plan. 及同一字符串表里的账单句，Drive 与 Classify 认这张卡。无头 stderr 的 quota exceeded 与无头 error JSON 贴到 PTY 上不是这张卡。崩溃只认宿主 WaitStatus 事实 ConsumeGrokProcessExit。Classify 与 ParseUsage 用 v1 样例映射到现有 ErrorClass 和 Usage。Capabilities 只有 grok 声明 goal_mode。WritablePaths 为 ~/.grok、~/.cursor、~/.kimi-code。

## 没做什么，以及原因
11 月 1 日曾运行一次真实 grok CLI，该采集已被拒收，相关文件已删除。未实现 PTY，未改 Runner、LaunchSpec、model，未移植 workflow、准入、路由、额度冷却和鉴权探测。LaunchSpec 表达不了的只读 plan、ACP、resume 未移植。不声明 web：--disable-web-search 不是证据。agy 的 WritablePaths 为空，v1 未给 antigravity 命名 CLI home（~/.gemini 属于已退役的 Gemini）。不把凭据从真实 CLI home 拷进 Kimi 隔离目录，因为那一步要读 ~/.kimi-code。v1 没有另写缓存路径，不猜测。

## 接口请求
LaunchSpec 缺 goal_mode/requires：用 Grok.GoalMode；Drive 无 prompt 参数，用 Grok.Prompt。宿主要把 goal 会话目录传给 Grok.SessionDir（目录里有 goal/state.json 与 updates.jsonl）。Drive 的 ReadWriter 只是 PTY，完成和 budget_limited 不在这上面。缺 effort：grok 省略 --reasoning-effort；agy 用 v1 空配置默认 high，thinking 模型省略；kimi 省略 KIMI_MODEL_THINKING_EFFORT。缺 session，不含 --resume/-s。缺 bin 覆盖，argv[0] 为 grok、cursor-agent、kimi、agy。缺只读/写：Build 一律写能力的沙箱内自动放行，不移植 --mode ask 与 plan。Build 不能返回进程 cwd：无头 grok、kimi、agy 的 WorkDir 只作 host 工作目录；cursor 用 --workspace，goal grok 用 --cwd。Kimi 凭据由宿主放进隔离 KIMI_CODE_HOME；适配器不读取 ~/.kimi-code。隔离 config.toml 的基础内容（default_model 与 provider 表）也由宿主写好，Build 只覆盖顶层 default_permission_mode 与 default_plan_mode。未设置 Kimi.ConfigHome 时 Build 自建空的隔离目录，没有凭据。交互额度卡是 grok TUI 字符串表里的 You've hit the rate limit for your plan. 及同表账单句，v1 runHostedGrokGoal 把该 TUI 放在 PTY 上；fakeGrokBuildCounted 的无头 stderr 不是这张卡。Classify 分不清 /goal 回显的下一行和真正的诊断，这一行不作为错误事实。无头 {"type":"error","message":"HTTP 429: usage limit reached"} 来自 grok_test.go fakeGrokBuild，贴到 PTY 上不是额度终态。崩溃要宿主在 Wait 之后把 WaitStatus 的 signaled 与 signal 号传给 ConsumeGrokProcessExit，只认 quit/aborted/killed/bus/segmentation fault（3、6、9、10、11，对应 v1 崩溃字符串）；Drive 的 ReadWriter 看不到该事实，本 lane 不改 runner.go。PTY 或正文里的 signal: killed 不是该事实。model.Usage 无重置时间，v1 的 epoch 与 resets at 放不进。input_too_long 与 executor_crash 无 ErrorClass，崩溃由 ConsumeGrokProcessExit 返回 ErrGrokCrash，Classify 为空。裸子串 quota 不判 quota_hit。Build 只返回适配器自己的环境变量。

## 已知问题
N2：AGY 嵌套错误 {"error":{"message":"quota exceeded"}} 与多行 JSON 的 usage 仍未按 v1 完整文档提取，不在本次 findings。
无头 stderr 的 quota exceeded 与无头 HTTP 429 JSON 贴到 PTY 上仍不是 ErrGrokQuotaHit。377 份 goal/state.json 没有额度 status，ReadGrokGoalSession 不发明额度状态。

## 修复记录
接收 prompt 正文的参数必须是文件内容，不能是路径 → internal/runner/grok.go:78、cursor.go:34、kimi.go:45、agy.go:40 → TestPromptFileBody
goal 的完成和 budget_limited 只从会话文件读取，PTY Read 阻塞时轮询、EOF 后再查，PTY 上的 session/update JSON 不算这两种终态 → internal/runner/grok.go:183、grok.go:232、grok.go:203、grok.go:559 → TestGrokDriveTerminals、TestGrokDriveSessionAfterReadStarts
正文、输入回显和未收完整的记录不是失败事实，只认完整的终态记录或错误记录 → internal/runner/grok.go:327、grok.go:340、grok.go:457（cursor.go:52、kimi.go:112、agy.go:44 经 classifyScanned） → TestBodyEchoNestedAreNotFacts
沙箱内的写运行必须自动放行，不能停在等人审批的参数上 → internal/runner/agy.go:40、kimi.go:39、kimi.go:62 → TestAGYBuildSnapshot、TestKimiAutoPermissionConfig
终态只认该模式实际提供的通道：完成和 budget_limited 只认会话文件，额度不把无头 error JSON 当交互样本，崩溃只认宿主 WaitStatus，PTY 正文 signal: killed 不算 → internal/runner/grok.go:56、grok.go:163 → TestGrokQuotaAndCrashChannels、TestGrokDriveSessionAfterReadStarts
正文、任务回显和嵌套业务字段不是终态或错误类，goal status 只读 goal_updated 的 params.update.status → internal/runner/grok.go:240、grok.go:340、grok.go:524（cursor.go:52、kimi.go:112、agy.go:44 经 classifyScanned） → TestBodyEchoNestedAreNotFacts
自动权限只覆盖顶层 default_permission_mode 与 default_plan_mode，保留宿主已写入的其余 config.toml → internal/runner/kimi.go:62、kimi.go:77 → TestKimiAutoPermissionConfig
准则 1 仍未完成：没有可引用的交互额度事实，Drive 与 ReadGrokGoalSession 不返回 ErrGrokQuotaHit，没有伪造样本；完成仍是一致会话的 nil，budget_limited 仍是另一种会话错误，崩溃仍只认信号 3、6、9、10、11 → internal/runner/grok.go:162、grok.go:598、grok.go:56 → TestGrokInteractiveQuota
正文和多行 /goal 回显及其分片不是 quota_hit 或 budget_limited，只认 JSON 错误记录字段和整行诊断 → internal/runner/grok.go:340、grok.go:457、grok.go:240（cursor.go:52、kimi.go:112、agy.go:44 经 classifyScanned） → TestBodyEchoNestedAreNotFacts
交互额度只接受能追溯到交互通道的样本；v1 无该转录，Drive 不把无头 stderr 的 quota exceeded 当成额度终态，完成、budget_limited 和宿主信号崩溃仍各走各的通道 → internal/runner/grok.go:162、grok.go:198 → TestGrokInteractiveQuota、TestGrokQuotaAndCrashChannels
正文、/goal 回显及其下一行、以及尚未换行的分片不能只因错误措辞成为错误事实，EOF 的末行才是完整记录 → internal/runner/grok.go:323、grok.go:344、agy.go:54、agy.go:108（cursor.go:52、kimi.go:112、agy.go:44 经 classifyScanned） → TestBodyEchoNestedAreNotFacts、TestRecordBoundaryDiagnostics、TestGrokInteractiveQuota
整行诊断仍按已有类匹配器映射：dial/i/o timeout/error sending request 为 transport，认证错误: 请重新登录 为 auth_failed；同一措辞在正文或回显里不分类 → internal/runner/grok.go:264、grok.go:267、grok.go:268、grok.go:372、grok.go:385、grok.go:404（cursor.go:52、kimi.go:112、agy.go:44 经 classifyScanned） → TestRecordBoundaryDiagnostics
额度终态只接受可追溯的交互转录或已有会话额度字段；372 份真实 goal/state.json 的 status 只有 complete/active/budget_limited/user_paused/no_progress_paused/infra_paused/blocked/back_off_paused，history 的 budget_exceeded 仍是 budget_limited，无额度终态，B2 仍未完成 → internal/runner/grok.go:162、grok.go:598 → TestGrokInteractiveQuota
正文、/goal 回显和真实诊断按来源区分：回显是已知 prompt，或无 prompt 时从 /goal 延续到完整 JSON 错误记录，不以空行或固定行数截断；冒号标签只认已知诊断来源 → internal/runner/grok.go:229、grok.go:348、grok.go:364、grok.go:382、grok.go:457、grok.go:466（cursor.go:52、kimi.go:112、agy.go:44、agy.go:108 经 classifyScanned） → TestSourceEchoAndLabels
已确认的 prompt 回显行不是诊断，包括 JSON 错误记录和其他 JSON；回显之后的同一记录仍分类 → internal/runner/grok.go:399、grok.go:573（cursor.go:53、kimi.go:113、agy.go:45 经 classifyScanned） → TestSourceEchoAndLabels
B2 未完成：额度终态只接受可追溯的交互转录或 v1 已写会话额度字段，observeNativeGrokGoal 只读 grokNativeGoalStateFile.Status，isLimitHitGrokBuild 只扫无头样本，不伪造 fixture → internal/runner/grok.go:162、grok.go:198、grok.go:640 → TestGrokInteractiveQuota
终态验收必须在可信交互通道上正向识别额度，排除不可信样本不能替代该验收 → internal/runner/grok.go:195、grok.go:243、grok.go:577 → TestGrokGoalQuotaTerminal
终态正向验收必须覆盖实际交互通道的输出，字符串表只能证明文案存在 → 未改 internal/runner/grok.go:194、grok.go:243、grok.go:577、grok.go:597 与 testdata/grok_goal_quota_pty.txt:1：v1 observeNativeGrokGoal（workflow_goal.go:2928）不读额度字段，380 份 goal/state.json 的 status 只有 complete/active/budget_limited/user_paused/no_progress_paused/blocked/back_off_paused/infra_paused，workflow_goal_host_unix.go:240 不留 PTY 转录 → TestGrokGoalQuotaTerminal
交互终态的正向验收必须覆盖实际 PTY 输出及其消费路径；字符串表文案、手写整行、或“没有转录”的说明都不能充当该验收 → internal/runner/grok.go:195、grok.go:244、grok.go:607、grok.go:632、grok.go:759 → TestGrokGoalQuotaTerminal
拒收的真实 CLI 采集不留在树里，历史不改写 → docs/v2/lane-notes/R2-B.md:7、internal/runner/grok_test.go:693 → TestGrokGoalQuotaTerminal
交互额度的正向验收只回放已有 screen 日志里的终端帧，并写明帧来源与嵌入的额度句 → internal/runner/grok.go:632、grok.go:767、testdata/grok_goal_quota_screen.provenance.txt:1 → TestGrokGoalQuotaTerminal
屏幕解析沿用行解析的回显和正文排除，被帧截断的行不是完成的额度诊断 → internal/runner/grok.go:437、grok.go:715、grok.go:767（cursor.go:52、kimi.go:112、agy.go:44 无屏幕通道） → TestGrokScreenEchoAndTruncation
屏幕回显来源必须随滚屏保留，屏幕文本与 prompt 用同一种 TrimSpace 比较，滚屏或显示缩进不能改变终态 → internal/runner/grok.go:452、grok.go:719、grok.go:793、grok.go:815（cursor.go:52、kimi.go:112、agy.go:44 无屏幕通道） → TestGrokScreenEchoAndTruncation
截断标志必须反映当前行内容，写满最后一列的重绘清除旧标志，仍超出末列的正文保持截断 → internal/runner/grok.go:736、grok.go:742（cursor.go:52、kimi.go:112、agy.go:44 无屏幕通道） → TestGrokScreenEchoAndTruncation
