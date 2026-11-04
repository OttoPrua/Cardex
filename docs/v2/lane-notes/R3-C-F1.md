# R3-C-F1

## 做了什么
sched.New 安装 loop.Dispose。deliver 只在该调用返回 nil 后写入 attempt 与卡；Dispose 为空则返回错误且不消费。dev、review、normalize 与 disposeAll 共用 decideHarvest，也就是既有 apply/Decide。设计类失败，以及带卡清单的通过 plan/replan，走同一条 dev 处置；其余设计类通过走 absorbAttempt。
畸形定时枚举就地取默认值，单条入队失败写入 fault 后继续其他任务。定时身份是安全名加原名 sha256 前 8 字节。冲突只看同一项目仓库，并跳过卡自身。同目标的 Step 在既有目标锁内跑验收。未知或无上限窗口不填充。无法落进账本的快照用量把置信度降下来。同一笔事件已经覆盖的嵌套用量不记为遗漏。

## 没做什么，以及原因
未改 cmd、internal/e2e、internal/quota、internal/claims、internal/model、goals、design。sched 不含 pass/rework 或 §5 判定。设计类失败和带清单的通过 plan/replan 用既有 dev 处置，其余设计类通过用 absorbAttempt。同目标串行沿用 queue.go 的目标锁，未改合并队列。

## 接口请求
S19 夹具仍无上限：`internal/e2e/scenarios_test.go:971` 的 RemainingWindow 只有 Ratio 0.9、Reset 为现在加 30 分钟、Confidence 1。`internal/e2e/harness.go:110` 存成没有 LimitTokens 的 5h 窗口。填充不把无上限正剩余当确认余量，端到端缺口仍开着。本次 `go test ./...` 的 TestS19 在 20s 超时，back 仍为 ready。请给该窗口上限和实际用量，例如 5h LimitTokens=100、UsedTokens=10，ResetAt 仍在 30 分钟内。RemainingWindow 或 harness 要记下 LimitTokens 与 UsedTokens。置信度为 1 的无上限 Remaining 不能再当成可填充。host 子命令、prompt.Build、notify.Stub、model.Budget 时长、Attempt 重置时间、runServe 改接 sched.Serve 仍见 R3-C.md。

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
无上限的正剩余不作为填充余量 → internal/sched/loop.go:805 → TestQuotaEstimateDrivesDispatchAndFill
无法同时表示的嵌套快照不是置信度 1 的满额 → internal/sched/loop.go:1341 internal/sched/quota_seed.go:65 → TestNestedSnapshotIsNotConfidentFull
R3-C.md 不超过 40 行并保留接口请求与已知问题 → docs/v2/lane-notes/R3-C.md:11 → TestR3CLaneNoteStaysWithinLimit
无上限的已存剩余比例不是填充余量；可确认估算要同时满足上限、比例和临近重置 → internal/sched/loop.go:805 → TestBacklogFillStoredRatioWithoutCap
设计类失败结局必须先做设计第 5 节的处置，成功后才消费 → internal/loop/dispose.go:145 internal/loop/dev_apply.go:74 → TestDesignFamilyFailureDisposition
成功的 plan/replan 必须先应用卡清单并吸收设计产出，然后才消费 → internal/loop/dispose.go:148 internal/loop/dev_apply.go:481 → TestPlanAndReplanListsBothPaths
本次收割实际改了节点文档或 context.md 的 design/plan/replan 要进入既有合并队列，用本次 claims，不能先标 done → internal/loop/design.go:1429 internal/loop/design.go:1442 → TestDesignFileEntersIntegration
normalize 的 Deps[0] 原卡边不挡住派发，其他依赖和停止状态仍然挡住 → internal/sched/loop.go:1096 → TestNormalizeStartsThenDisposes
