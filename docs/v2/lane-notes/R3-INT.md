# R3-INT

## 做了什么
按顺序用 `git merge --no-ff` 合入 `v2-R3-C`、`v2-R3-D`、`v2-R4-A`。`scenarios_test.go` 留下原有的 S06–S18、S24、S26，接上 R3-C 的 S02–S05、S19、S20 和 R4-A 的 S22。S01、S21、S23 仍 skip。`claims.Conflicts` 的 exists 传 nil，`prompt.Build` 出错就让 tick 失败，只有成功且正文为空才回退标题。`sched.New` 的 Dispose 仍为空。合入前 loop 测试有两个 `writeResult`，设计侧改名为 `writeResultFile`。已实现的 `DevStep` 在没有 `CARDEX_HOME` 时不再被当成 `ErrNotImplemented`。

## 没做什么，以及原因
未合入 `v2-R2-B`。未改 `docs/v2/goals.md` 与 `docs/v2/design.md`。未 push。未安装收割处置，未把 pass/rework 抄进 sched，也未把 `runServe` 换成 `sched.Serve`。

## 接口请求
请 R3-B 导出 `Dispose(ctx, db, card, res)`，只读 `## 复核结论`。在此之前 `sched.New`（internal/sched/loop.go:65）、`Serve` 和 CLI `sched.New`（cmd/cardex/main.go:178）保持 Dispose 为空。`runServe` 整段替换仍按 R3-C 的请求，本 lane 只继承了那一行构造。

## 已知问题
e2e 的 `scenarioDispose` 仍按全文第一个 pass/rework 单词处置，S02–S05、S19、S20 依赖它；生产调度不安装它。调度默认仍调用 `notify.Stub`，真实投递留在 notify 包。

## 修复记录
按顺序 no-ff 合入时，冲突保留双方已落地场景，不用后合入的 skip 盖掉先合入的实现。 → internal/e2e/scenarios_test.go:41, internal/e2e/scenarios_test.go:87, internal/e2e/scenarios_test.go:136, internal/e2e/scenarios_test.go:199, internal/e2e/scenarios_test.go:257, internal/e2e/scenarios_test.go:724, internal/e2e/scenarios_test.go:773, internal/e2e/scenarios_test.go:829, internal/e2e/scenarios_test.go:895, internal/e2e/s26_research_test.go:134 → TestS02, TestS03, TestS04, TestS05, TestS06, TestS19, TestS20, TestS22, TestS24, TestS26
本 worktree 的 build、vet、test 必须通过且改动已提交。 → internal/sched/loop.go:514, internal/sched/loop.go:929, internal/loop/design_harvest_test.go:465, internal/loop/design_rework_round_test.go:690, internal/loop/design_test.go:138 → TestPromptFailureResumesSameBranch, TestTickDispatchClaimsAndSecondTick, TestProductionHarvestLeavesDispositionPending, TestDesignStepNeedsStoreAndDevStaysOpen, TestS02
