# R2-A

## 做了什么
实现 macOS/Linux PTY（标准库与 syscall）和 macOS sandbox-exec：默认可读，写限传入路径、TMPDIR 和必要的 /dev，允许网络。非 macOS 不包裹，并记一条 sandbox_skipped。`cardex host <attempt-id>` 从 store 读取 attempt、card、route 和 PromptPath，经 runner.Build 与 sandbox.Wrap 启动；仅在需要 PTY 时调用 Drive。标准输出和标准错误整段写入 `$CARDEX_HOME/attempts/<id>/output.log`。墙钟上限读 config.json 的 `host.hard_duration`，到期后 SIGKILL 整个进程组。写回退出码、结束时间、ParseUsage 用量和宿主 PID，并调用 workspace.Manager.Commit；Commit 失败仍写回。host.Reconcile 不改动仍存活的宿主，对已死 PID 写下结束时间且不收割、不改 outcome。注册 fake、claude、codex、opencode。三个真实执行器的 Capabilities 为空。claude engines 档案经环境变量注入（含 ANTHROPIC_BASE_URL），并清掉另一档案留下的 ANTHROPIC_*。Classify 与 ParseUsage 按 v1 样本映射到现有 ErrorClass 和用量形状。WritablePaths 只声明各 CLI 的 home 与缓存目录。S24 在 macOS 上由 sandbox-exec 拒绝 worktree 外的创建。

## 没做什么，以及原因
未运行真实 claude、codex、opencode。未做路由和收割（R2-C、R2-D）。未实现 workspace 的 git commit、diff 和阻止 push（R2-C）。未接入 grok、cursor、kimi、agy。未新增 ErrorClass。Linux 上的 PTY 与 sandbox 只做了交叉编译，没有执行。

## 接口请求
model.Budget 只有 SoftTokens 和 HardTokens。请增加墙钟时长字段，或确认进程组超时继续由宿主本地 config.json 的 host.hard_duration 决定，不要把 token 数当成秒。LaunchSpec 没有 engines 档案正文，claude 档案 JSON 暂时放在 Script；请给档案单独字段。v1 的 input_too_long、executor_crash、in_flight、unknown、cancellation 在 v2 没有对应 ErrorClass，目前保持未分类。

## 已知问题
workspace.Stub.Commit 仍返回未实现；host 记下该错误并照常写回退出码、结束时间和用量。Classify 只能看到退出码和整段输出，转录里的同类字样可能被误判。token 预算不会启动计时器。
