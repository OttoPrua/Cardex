# R2-A

## 做了什么
macOS/Linux PTY 与 macOS sandbox-exec（默认可读，写限传入路径、TMPDIR 和必要的 /dev，允许网络；非 macOS 不包裹并记 sandbox_skipped）。`host.Run` 读取 attempt、card、route 和 PromptPath，经 runner.Build 与 sandbox.Wrap 启动。执行器进程一开始就按本次尝试的墙钟期限监控，覆盖 Drive、父进程退出后的后代，以及仍占着输出管道的后代；到期 SIGKILL 进程组，写回 `timeout` 和 hard-limit timeout。期限来自 `$CARDEX_HOME/attempts/<id>/hard-duration`，不读 token，也不读已删除的全局期限键。缺失或无法解析时记 `budget_duration`，不静默取消。PTY 只读一次 master，同一份字节写入 `output.log` 并交给 Drive；Drive 的错误写入 attempt。启动后把执行器进程组写入 `executor.pgid`。宿主已死而进程组仍在时 Reconcile 不写 EndedAt，组退出或清理后再报告结束。claude 的 engines 档案缺失、未知或无法解析时记具体原因，卡改为 `needs_input`，不启动默认 Claude。Codex argv 含 `--json`。Claude/Codex/OpenCode 的真实 usage 不被正文 `tokens=N` 覆盖；该标记只由 fake 认。分类只看结构化终态、`error:` / `outside write denied:` 诊断和进程结果。S24 在 macOS 上拒绝 worktree 外的创建。

## 没做什么，以及原因
未运行真实 claude、codex、opencode。未做路由和收割。未实现 workspace 的 git commit、diff 和阻止 push。未接入 grok、cursor、kimi、agy。未新增 ErrorClass，未改 model/store。Linux 上的 PTY 与 sandbox 只做了交叉编译。

## 接口请求
model.Budget 增加 `HardDuration string`（Go duration，例如 `45m`）。store 的 cards 与 objectives 增加 `hard_duration` 文本列，随 Budget 读写。把 `internal/host/deadline.go` 的 `attemptDeadline` 改为解析 `card.Budget.HardDuration`。不要把 HardTokens 当成秒，不要恢复 config.json 的 `host.hard_duration`。字段落地前，调度方把本次时长写成 `$CARDEX_HOME/attempts/<id>/hard-duration`（纯文本）。缺失或无法解析时 host 已记 `budget_duration`。

LaunchSpec 增加 engines 档案字段，替代暂存在 Script 的 JSON。

`cardex host <attempt-id>` 接线已从本分支拆出，请集成者加回：`cmd/cardex/main.go` 的 `host` 分支改为 `cmdHost`。`cmdHost` 要求恰好一个 attempt id，`resolveHome` 后调用 `host.Run(context.Background(), home, id)`；失败写 stderr 并返回 1，缺少 id 返回 2。`cli_test.go` 去掉 `host` 的 unimplemented 行，改为跑 fake 尝试并检查 `output.log` 与 `tokens`。

v1 的 input_too_long、executor_crash、in_flight、unknown、cancellation 在 v2 没有对应 ErrorClass，保持未分类。

## 已知问题
output.log 仍在尝试结束时一次写完，宿主中途崩溃会丢掉已经读到的输出。context 取消后 finish 仍用同一个 ctx 调用 Commit 和 UpdateAttempt，退出码和用量可能写不回。registry 测试仍断言 grok 未注册，R2-B 接入时会冲突。workspace.Stub.Commit 仍返回未实现；host 记下该错误并照常写回退出码、结束时间和用量。

## 修复记录
预算 hard 时限覆盖执行器及其进程组的整个生命周期，期限不是全局配置也不是 token 秒数；缺失或无法解析要留下原因 → `internal/host/deadline.go:24` `internal/host/deadline.go:85` `internal/host/run.go:106` `internal/host/run.go:230` `internal/host/run.go:263` `internal/host/run.go:387`（`internal/host/config.go` 不再提供该键） → `TestDistinctAttemptDeadlines` `TestDescendantHoldingPipeCannotOutliveDeadline` `TestDeadlineCoversPTYDrive` `TestDurationMissingOrUnparseableIsRecorded` `TestRunKillsProcessGroup` `TestHardTokensDoNotArmTimer`
PTY 只读一次 master，日志和 Drive 拿到同一份字节，Drive 的错误要写回 → `internal/host/run.go:209` `internal/runner/fake.go:141` → `TestPTYDriveReceivesTerminalBytes` `TestPTYDriveErrorIsApplied`
宿主死亡不等于执行器已经结束；进程组仍在时不写 EndedAt → `internal/host/deadline.go:43` `internal/host/reconcile.go:34` → `TestReconcileDeadHostWithLiveChild` `TestReconcileLiveAndDead`
已选渠道的 engines 档案缺失、未知或无法解析时不启动默认 Claude，并交给复核 → `internal/host/config.go:37` `internal/host/run.go:78` `internal/host/run.go:365` → `TestUnresolvedEngineProfileDoesNotStartClaude`
用量解析与实际启动格式一致，真实 usage 不被正文里的 fake 标记覆盖 → `internal/runner/codex.go:35` `internal/runner/usage.go:16` `internal/runner/fake.go:193` → `TestCodexBuildMatchesTurnCompletedUsage` `TestRealUsageIgnoresFakeMarker` `TestParseUsageShapes`
错误分类只依据结构化终态、明确诊断和进程结果，不把任务正文里的错误词当失败 → `internal/runner/classify.go:51` `internal/runner/claude.go:23` `internal/runner/codex.go:22` `internal/runner/opencode.go:23` `internal/runner/fake.go:187` → `TestClassifyIgnoresTaskBodyQuotes` `TestClassifyV1Samples`
CLI 接线不属于本 lane，拆出后把补丁留在接口请求 → `cmd/cardex/main.go` `cmd/cardex/cli_test.go`（相对 `12fab3a` 无差异） → `TestUnimplementedLanes`
