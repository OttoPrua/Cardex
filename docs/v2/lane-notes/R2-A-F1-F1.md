# R2-A-F1-F1

## 做了什么
`finish` 的 workspace Commit 仍用单独的有界 context：run 取消不会取消它，workspace manager 仍在。Commit 返回后，尝试更新、`host_finish` 和 `workspace_commit` 另开一个有界 context。这个 context 从 run context 去掉取消后再限时，不是已经耗尽的提交 context 的子 context。管道和 PTY 共用这一条 finish。

## 没做什么，以及原因
未改 failStart、Reconcile 和 needs_input 交接，它们不提交。未缩短生产上的 30s 落库期限。未改执行器硬期限、沙箱、PTY、注册表和真实 CLI 适配器。未改取消 run 时把错误类标成 timeout 的分类。

## 接口请求
无。

## 已知问题
取消 run 时 `armGroup` 仍可能把 fired 置上，finish 因此把错误类标成 timeout。本次只把提交期限和结果落库拆开。

## 修复记录
提交超时不应同时耗尽结果落库的期限。 → internal/host/run.go:393, internal/host/run.go:432, internal/host/run.go:437, internal/host/run.go:439, internal/host/run.go:454, internal/host/run.go:456, internal/host/run.go:458 → TestCommitDeadlineDoesNotDropExecutionResult
