# R3-C

## 做了什么
merge.Service 按目标加锁串行合并并跑验收。冲突派 rebase，测试变红派 repair。修复链收尾中断后仍能补完，已合并前驱的残留工作区会删掉。
sched.Runner.Tick 走 §6.1。收割只在 Dispose 成功返回后记为已消费。New 安装 loop.Dispose，Serve 和 CLI serve 经 New 继承。派发只豁免 review/repair/rebase 的关联边，并拒绝暂停、取消和其他不可派发状态。自动路由和填充共用额度估算。无上限窗口保持 unknown，填充不把它当确认余量。零用量和已刷新窗口不吸收其他窗口的快照。分支已存在但 attempt 未落库时，下次 Tick 续接该分支。
S02、S03、S04、S05、S19、S20 注入这条调度器。Harness 默认仍是 stub。

## 没做什么，以及原因
未实现设计 §6.2–§6.6。未改路由算法、看板、通知适配器、prompt 模板、quota 数学、cron-lite 或 internal/model。调度不解析 pass/rework，也不抄 §5。

## 接口请求
host 子命令仍打印未实现。调度设置 CARDEX_SCHED_HOST=1，在 init 里调用 host.Run。请把子命令接到 host.Run 后去掉这个环境变量。
prompt.Build 和 notify.Stub.Deliver 仍是 ErrNotImplemented。model.Budget 没有时长，调度把 30m 写到 attempts/<id>/hard-duration。
Attempt 没有撞线重置时间。调度只采用与 reset 关联的 RFC3339，过期记录不会清掉仍有效的封锁。请给尝试加可选重置时间。
请把 runServe 的 inline 循环换成 sched.Serve。本分支只把构造改成 `loop := sched.New(home)`（cmd/cardex/main.go:178）。

## 已知问题
rebase 和 repair 若没有 fake 脚本或 scripts/<id>.json，宿主仍会启动，但工作区可能没有解冲突或修好测试，合并会再次失败。
internal/e2e 的 scenarioDispose 仍按全文第一个 pass/rework 单词处置，并用固定 pass 脚本。S02、S03、S04、S05、S19、S20 依赖它。S19 的无上限窗口不再满足填充，夹具请求见 R3-C-F1.md。
quota.Ledger 只配置 5h/daily/weekly/monthly。其他窗口名即使写了上限也不会进入估算。
Ledger.Record 不能指定窗口。快照按各窗口的时间范围入账；较长窗口的已用量若小于嵌在里面的较短窗口，就无法精确表示。调度把无法落点的用量标成不确定。

## 修复记录
收割成功后才消费，New 安装 loop.Dispose → internal/sched/loop.go:67 → TestProductionHarvestLeavesDispositionPending
无上限窗口不是确认余量 → internal/sched/loop.go:683 → TestQuotaEstimateDrivesDispatchAndFill
派发只豁免关联边并拒绝停机状态 → internal/sched/loop.go:932 → TestDispatchChecksAssociationEdgeAndStopState
工作区已建但 attempt 未落库时续接 → internal/sched/loop.go:426 → TestWorkspaceExistsBeforeAttemptResumes
修复链中断后仍能补完 → internal/merge/queue.go:73 → TestInterruptedRepairChainStillCloses
窗口快照保留各自用量 → internal/sched/quota_seed.go:37 → TestWindowSnapshotKeepsItsOwnUsage
