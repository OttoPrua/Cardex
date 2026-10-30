# R2-B

## 做了什么
四个适配器实现 runner.Runner，Name 为 grok、cursor、kimi、agy。Build 移植 v1 写能力且沙箱内自动放行的 argv：grok 无头为 streaming-json、--sandbox workspace、--permission-mode auto、--prompt-file；cursor-agent 为 --print --trust --sandbox enabled，prompt 为位置参数；kimi 为 --model --prompt stream-json；agy 为 --mode accept-edits、json、--print，非 thinking 模型带 --effort high。普通 grok NeedsPTY 为 false。要求 goal_mode 时 grok NeedsPTY 为 true，Drive 写入字面量 /goal 与 prompt，并用错误区分完成、budget_limited、撞额度、崩溃。Classify 与 ParseUsage 用 v1 样例映射到现有 ErrorClass 和 Usage。Capabilities 只有 grok 声明 goal_mode。WritablePaths 为 ~/.grok、~/.cursor、~/.kimi-code。

## 没做什么，以及原因
未运行真实 CLI，未实现 PTY，未改 Runner、LaunchSpec、model，未移植 workflow、准入、路由、额度冷却和鉴权探测。LaunchSpec 表达不了的只读 plan、ACP、resume 未移植。不声明 web：--disable-web-search 不是证据。agy 的 WritablePaths 为空，v1 未给 antigravity 命名 CLI home（~/.gemini 属于已退役的 Gemini）。kimi 的自动放行在 v1 是隔离 config.toml，准备它必须读 ~/.kimi-code，本 lane 不读，故 argv 不含 --auto/--yolo。v1 没有另写缓存路径，不猜测。

## 接口请求
LaunchSpec 缺 goal_mode/requires：用 Grok.GoalMode；Drive 无 prompt 参数，用 Grok.Prompt。缺 effort：grok 省略 --reasoning-effort；agy 用 v1 空配置默认 high，thinking 模型省略；kimi 省略 KIMI_MODEL_THINKING_EFFORT。缺 session，不含 --resume/-s。缺 bin 覆盖，argv[0] 为 grok、cursor-agent、kimi、agy。缺只读/写：Build 一律写能力的沙箱内自动放行，不移植 --mode ask 与 plan。Build 不能返回进程 cwd：无头 grok、kimi、agy 的 WorkDir 只作 host 工作目录；cursor 用 --workspace，goal grok 用 --cwd。kimi 需要 host 提供隔离 KIMI_CODE_HOME（default_permission_mode=auto，default_plan_mode=false）。model.Usage 无重置时间，v1 的 epoch 与 resets at 放不进。input_too_long 与 executor_crash 无 ErrorClass，崩溃只由 Drive 的 ErrGrokCrash 区分，Classify 为空。裸子串 quota 不判 quota_hit。Build 只返回适配器自己的环境变量。v1 cursor/kimi/agy 传的是 prompt 正文，这里传 PromptPath。

## 已知问题
无。
