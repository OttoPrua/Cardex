# R3-C

## 做了什么
merge.Service 串行合并并跑验收。冲突派 rebase，测试变红派 repair。修复链收尾中断后仍能补完，已合并前驱的残留工作区会删掉。
sched.Runner.Tick 走 §6.1。收割只在处置成功后才记为已消费。Dispose 为空时，通过的开发卡进入复核，复核结论 pass 则把对象入队合并；其余结果保持待交付。派发只豁免 review/repair/rebase 的关联边，并拒绝暂停、取消和其他不可派发状态。自动路由和填充共用 quota.Ledger。无上限窗口保持 unknown；显式正剩余比例仍可填充。分支已存在但 attempt 未落库时，下次 Tick 续接该分支。
S02、S03、S04、S05、S19、S20 注入这条调度器。Harness 默认仍是 stub。serve 用 sched.New 执行原循环。

## 没做什么，以及原因
未实现设计 §6.2–§6.6。未改路由算法、看板、通知适配器、prompt 模板、quota 数学、cron-lite 或 internal/model。

## 接口请求
loop 仍只有 DesignStep 和 DevStep。复核与合并交接已在 sched 生产路径完成。§5 其余处置（返工、换渠道、待拍板等）请 loop 提供入口；调度只在处置成功时消费。
host 子命令仍打印未实现。调度设置 CARDEX_SCHED_HOST=1，在 init 里调用 host.Run。请把子命令接到 host.Run 后去掉这个环境变量。
prompt.Build 和 notify.Stub.Deliver 仍是 ErrNotImplemented。model.Budget 没有时长，调度把 30m 写到 attempts/<id>/hard-duration。
Attempt 没有撞线重置时间。调度只采用与 reset 关联的 RFC3339，过期记录不会清掉仍有效的封锁。请给尝试加可选重置时间。
请把 runServe 的 inline 循环换成 sched.Serve。本分支只把 `loop := sched.Stub{}` 改成 `loop := sched.New(home)`（cmd/cardex/main.go:178）。拆出的替换：

```go
err = sched.Serve(sched.WithLog(ctx, stdout), home, addr)
if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) {
    return 0
}
fmt.Fprintln(stderr, err)
return 1
```

同时可删 net/http、time 和 cardex/internal/board 的引用。

## 已知问题
rebase 和 repair 若没有 fake 脚本或 scripts/<id>.json，宿主仍会启动，但工作区可能没有解冲突或修好测试，合并会再次失败。
quota.Ledger 只配置 5h/daily/weekly/monthly。其他窗口名即使写了上限也不会进入估算。
Ledger.Record 不能指定窗口。快照按各窗口的时间范围入账；较长窗口的已用量若小于嵌在里面的较短窗口，就无法精确表示。

## 修复记录
未成功交给处置入口的结果不能记为已消费 → internal/sched/loop.go:173 → TestHarvestStaysPendingUntilDisposition
自动路由和填充使用当前额度估算，含尝试用量、刷新、请求上限和撞线封锁 → internal/sched/loop.go:303,603,1084 → TestQuotaEstimateDrivesDispatchAndFill
无上限窗口不是已知零余额，也不合成 token 上限 → internal/sched/loop.go:685,1084 → TestQuotaEstimateDrivesDispatchAndFill
派发和恢复只豁免关联边，并拒绝暂停、取消及其他不可派发状态 → internal/sched/loop.go:476,944,953 → TestDispatchChecksAssociationEdgeAndStopState
工作区已建但 attempt 未落库时续接同一分支 → internal/sched/loop.go:415 → TestWorkspaceExistsBeforeAttemptResumes
修复链收尾中断后仍能补完，已合并前驱也会删残留工作区 → internal/merge/queue.go:75,180,222 → TestInterruptedRepairChainStillCloses
runServe 只改调度器构造那一行，整段替换交给集成 → cmd/cardex/main.go:178 → TestServeLoopsUntilStopped
通过的开发收割进入复核，复核结论 pass 则入队合并；处置失败保持待交付 → internal/sched/loop.go:191,220 internal/sched/serve.go:65 → TestProductionDeliveryHandsOffReviewAndMerge
窗口快照保留各自已用量，不把最大用量广播到每个窗口 → internal/sched/quota_seed.go:36,60 internal/sched/loop.go:1200 → TestWindowSnapshotKeepsItsOwnUsage
仍有效的撞线封锁按观测时间保留，只取与 reset 关联的时间 → internal/sched/quota_seed.go:160,180 internal/sched/loop.go:1233,1237 → TestQuotaHitBlockFollowsResetNotOrder
