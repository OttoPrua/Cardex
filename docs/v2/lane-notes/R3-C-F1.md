# R3-C-F1

## 做了什么
sched.New 安装 loop.Dispose。deliver 只在该调用返回 nil 后写入 attempt 与卡；Dispose 为空则返回错误且不消费。dev、review、normalize 与 disposeAll 共用 decideHarvest，也就是既有 apply/Decide。design、research、plan、replan、spike 走已有 absorbAttempt。
畸形定时枚举就地取默认值，单条入队失败写入 fault 后继续其他任务。定时身份是安全名加原名 sha256 前 8 字节。冲突只看同一项目仓库，并跳过卡自身。同目标的 Step 在既有目标锁内跑验收。未知或无上限窗口不填充。无法落进账本的快照用量把置信度降下来。

## 没做什么，以及原因
未改 cmd、internal/e2e、internal/quota、internal/claims、internal/model、goals、design。sched 不含 pass/rework 或 §5 判定。未新增 §5 分支：每一行已有 Decide 与 apply，设计类走 absorbAttempt。同目标串行沿用 queue.go 的目标锁，未改合并队列。

## 接口请求
TestS19 仍失败：`internal/e2e/scenarios_test.go:731` 的 RemainingWindow 只有 Ratio 0.9、Reset 为现在加 30 分钟、Confidence 1。`internal/e2e/harness.go:110` 存成没有 LimitTokens 的 5h 窗口。填充不把无上限正剩余当确认余量，卡 back 停在 ready，20s 超时。请给该窗口上限和实际用量，例如 5h LimitTokens=100、UsedTokens=10，ResetAt 仍在 30 分钟内。RemainingWindow 或 harness 要记下 LimitTokens 与 UsedTokens。置信度为 1 的无上限 Remaining 不能再当成可填充。host 子命令、prompt.Build、notify.Stub、model.Budget 时长、Attempt 重置时间、runServe 改接 sched.Serve 仍见 R3-C.md。

## 已知问题
e2e 的 scenarioDispose 仍按全文第一个 pass/rework 处置。rebase 与 repair 缺少脚本时宿主仍会启动。
Ledger.Record 不能把一笔用量只记入较短窗口。嵌套 5h/daily 里无法落点的部分只在调度估算里标成不确定。
一次 `go test ./...` 里 TestPTYDriveErrorIsApplied 失败；单独重跑三次通过。本 lane 未改 internal/host。

## 修复记录
默认收割交给 loop.Dispose，空钩子不消费 → internal/sched/loop.go:67 internal/loop/dispose.go:55 internal/loop/dev_apply.go:72 → TestDefaultHarvestDisposition
畸形定时枚举就地默认，单条失败不终止 Tick → internal/sched/loop.go:751 internal/sched/loop.go:846 → TestScheduleTierT9DoesNotFailTick
不同定时名保持含原名摘要的去重身份 → internal/sched/loop.go:810 → TestScheduleNamesKeepDistinctIdentities
声明占用只看同一项目仓库 → internal/sched/loop.go:973 → TestDispatchSameSymbolTwoRepos
恢复派发不把卡自身算作其他占用者 → internal/sched/loop.go:948 → TestClaimedRunningCardResumes
同目标并发 Step 的验收区间不重叠 → internal/merge/queue.go:61 → TestQueueSameTargetAcceptanceIntervalsDoNotOverlap
无上限的正剩余不作为填充余量 → internal/sched/loop.go:683 → TestQuotaEstimateDrivesDispatchAndFill
无法同时表示的嵌套快照不是置信度 1 的满额 → internal/sched/loop.go:1215 internal/sched/quota_seed.go:70 → TestNestedSnapshotIsNotConfidentFull
R3-C.md 不超过 40 行并保留接口请求与已知问题 → docs/v2/lane-notes/R3-C.md:11 → TestR3CLaneNoteStaysWithinLimit
临近重置的已存剩余比例在没有 token 或请求上限时仍是填充余量，既无上限也无剩余比例的窗口仍未知 → internal/sched/loop.go:685 internal/sched/loop.go:695 internal/sched/loop.go:705 → TestBacklogFillStoredRatioWithoutCap
