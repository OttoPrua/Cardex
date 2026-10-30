# R2-A

## 做了什么
macOS/Linux PTY 与 macOS sandbox-exec（默认可读，写限传入路径、TMPDIR 和必要的 /dev，允许网络；非 macOS 不包裹并记 sandbox_skipped）。`host.Run` 读取 attempt、card、route 和 PromptPath，经 runner.Build 与 sandbox.Wrap 启动。执行器进程组从启动到退出都受本次 `hard-duration` 约束；关掉标准输出的后代到期也会被 SIGKILL，并记 `timeout`。期限文件缺失、为空、无效或非正数时卡改为 `needs_input`，不启动。不读 token，也不读已删除的全局期限键。PTY 只读一次 master；Drive 在完成、`budget_limited` 或错误处返回后，后续输出仍写入 `output.log`，超时和取消会解开阻塞的读写。宿主死亡后，Reconcile 在本次硬期限到达时清理仍在的进程组，确认退出再写 EndedAt；期限未到或宿主仍在则不动。已选 engines 档案在 host 与 Claude Build 用同一可用性判断；空对象、未知字段或空白字段走 `needs_input`，不回落默认渠道。Codex argv 含 `--json`。真实 usage 不被正文 `tokens=N` 覆盖。分类只看结构化终态和 `outside write denied:`，正文里的 `error:` 行不算，更早的不可分类错误不挡住后面的终态。S24 在 macOS 上拒绝 worktree 外的创建。

## 没做什么，以及原因
未运行真实 claude、codex、opencode。未做路由和收割。未实现 workspace 的 git commit、diff 和阻止 push。未接入 grok、cursor、kimi、agy。未新增 ErrorClass，未改 model/store。Linux 上的 PTY 与 sandbox 只做了交叉编译。

## 接口请求
model.Budget 增加 `HardDuration string`（Go duration，例如 `45m`）。store 的 cards 与 objectives 增加 `hard_duration` 文本列，随 Budget 读写。把 `internal/host/deadline.go` 的 `attemptDeadline` 改为解析 `card.Budget.HardDuration`。不要把 HardTokens 当成秒，不要恢复 config.json 的 `host.hard_duration`。字段落地前，调度方把本次时长写成 `$CARDEX_HOME/attempts/<id>/hard-duration`。缺失、为空或非正数时 host 把卡标为 `needs_input`，不启动。

LaunchSpec 增加 engines 档案字段，替代暂存在 Script 的 JSON。

`cardex host <attempt-id>` 接线已从本分支拆出，请集成者加回：`cmd/cardex/main.go` 的 `host` 分支改为 `cmdHost`。`cmdHost` 要求恰好一个 attempt id，`resolveHome` 后调用 `host.Run(context.Background(), home, id)`；失败写 stderr 并返回 1，缺少 id 返回 2。`cli_test.go` 去掉 `host` 的 unimplemented 行，改为跑 fake 尝试并检查 `output.log` 与 `tokens`。

v1 的 input_too_long、executor_crash、in_flight、unknown、cancellation 在 v2 没有对应 ErrorClass，保持未分类。

## 已知问题
output.log 仍在尝试结束时一次写完，宿主中途崩溃会丢掉已经读到的输出。context 取消后 finish 仍用同一个 ctx 调用 Commit 和 UpdateAttempt，退出码和用量可能写不回。registry 测试仍断言 grok 未注册，R2-B 接入时会冲突。workspace.Stub.Commit 仍返回未实现；host 记下该错误并照常写回退出码、结束时间和用量。

## 修复记录
B1 硬期限覆盖到进程组退出，无效期限不启动 → `internal/host/deadline.go:121` `internal/host/run.go:110` `internal/runner/fake_child.go:101` → `TestDescendantClosingStdioCannotOutliveDeadline` `TestDurationMissingOrUnparseableDoesNotStart` `TestHardTokensDoNotArmTimer` `TestDistinctAttemptDeadlines`
B2 Drive 返回后继续排空日志，超时和取消解开阻塞读写 → `internal/host/run.go:208` `internal/runner/fake.go:197` → `TestDriveReturnStillDrainsLog` `TestDriveBlockUnblockedByDeadlineOrCancel`
B3 失联宿主的进程组在硬期限后由 Reconcile 清理，确认退出再落库 → `internal/host/reconcile.go:35` `internal/host/deadline.go:166` → `TestReconcileReapsOrphanAfterHardDuration`
B4 已选档案在 host 与 Build 使用同一可用性判断，无效则 needs_input 且不回落默认 → `internal/host/config.go:52` `internal/runner/engines.go:226` `internal/runner/claude.go:45` → `TestUnresolvedEngineProfileDoesNotStartClaude` `TestUsableEngineProfileInjectsBaseURL` `TestEngineProfileValidity`
B6 分类忽略正文 error 行并扫描后续结构化终态 → `internal/runner/classify.go:52` `internal/runner/classify.go:150` `internal/runner/fake.go:394` `internal/host/run.go:415` → `TestClassifyBodyLineAndLaterTerminal` `TestClassifyV1Samples`
