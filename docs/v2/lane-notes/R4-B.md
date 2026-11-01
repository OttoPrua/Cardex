# R4-B

## 做了什么

补全 design §12。list/show 接受 `--json`。每条命令的帮助写明用途和参数，命令后的 `--help` 不再被全局帮助吞掉。`objective add` 和 `card add` 经 `notify.OriginFromArgs` 记录 origin，出现的 `--origin` 优先于 `CARDEX_ORIGIN`。目标写入已有字段；卡先记 `origin` 事件，show 再拼回。`question answer` 走 `question.New`。`positioning approve` 走 `positioning.Open`。`route explain` 打印该卡最大序号尝试上已存的 RouteReason，不调用 `route.Select`。`schedule list` 读 `config.json` 的 schedules；`schedule run` 按模板 CreateCard 一次，不 Tick、不启动执行器。`scorecard` 输出 `Compute` 的结果或包错误。`host` 把 attempt 交给 `host.Run`。`doctor` 走完全部检查，只打印问题和建议，发现项不改退出码。`import-legacy` 只读 v1，按 §14 落位；dry-run 只出计数；再次导入不复制卡或目标。S21 已通过。

## 没做什么，以及原因

没有改 store、model、sched、quota、positioning、scorecard、route、question、board。没有正式导入 `~/.cardex`，那是 R5-B。没有 launchd，也没有绑定 8787/8788。`Compute` 仍是 not implemented，CLI 不自己做成绩单。

## 接口请求

`model.Card` 没有 origin。CLI 把 `kind:id` 记成 `card:<id>` 的 `origin` 事件。请给卡加 origin 字段。

没有非绑定路由提示字段。导入把 `frozen_route` 和 `runner_pref` 记成 `route_hint` 事件，不写入 Channel 或 Model。请加提示字段，避免把提示当成显式指定。

sched 的入队未导出。请导出 `Enqueue(ctx, home, name)`，只入队该名字的模板，不 Tick、不启动执行器。

额度窗口的 canonName/canonRule 未导出。doctor 用 `quota.Ledger` 探测；未知重置规则会被收成 rolling，因此不报。请导出窗口名校验。

`positioning.Store` 没有 ListProposals。CLI 读 `positioning/proposals/*.json`。请导出列表。`Reject` 仍返回 unspecified，CLI 只交出这个错误。

`model.Card` 没有 step。可继续导入把 v1 step 写在正文首行 `step: N`，prompts 跟在后面。请加列。

## 已知问题

卡 origin 和路由提示只在事件里，其他包看不到。`schedule run` 不写调度戳，也不判断 cron 是否到期。doctor 不把未知重置规则当问题。`positioning reject` 按包的实现失败，且不改当前表。同一项目多条非空 dir/repo 时保留文件名排序后的第一条。复核里的 `TestPTYDriveReceivesTerminalBytes` 这次全量通过，`internal/host` 与 v2 无差异，没有改它。

## 修复记录

B1：续做正文保留每个 prompt 的步骤边界，有效 step 之前只作已完成上下文，当前 prompt（含内部换行）及之后的 prompt 为待执行 → internal/importer/place.go:156 → TestImportContinuableBodyKeepsStepBoundaries

B2：done/failed/canceled/unparsed 在 card retry/resume/revive/pause/cancel 上保持原记录，held 的 revive 仍变为 ready 并保留原因 → cmd/cardex/ops.go:221 cmd/cardex/ops.go:248 → TestImportedHistoryStaysReadOnly

B3：节点冻结和重开把本节点未答冻结确认写成同意，Pending、Blocked 和提醒读到非空 Answer；freeze 不追加 loop.reopen_used → cmd/cardex/surface.go:201 cmd/cardex/surface.go:245 cmd/cardex/surface.go:258 → TestNodeFreezeAndReopenCloseUnansweredFreeze

B4：scorecard 测试按 Compute 的真实返回断言退出码和输出，不要求未实现 → cmd/cardex/cli_surface_test.go:475 → TestScorecardSurfacesCompute

B5：go build、go vet、go test ./... 必须通过 → 未改 internal/host/rework_test.go:169（与 v2 无差异，本次全量退出 0） → TestPTYDriveReceivesTerminalBytes
