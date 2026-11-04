# R3-C-F1-F1

## 做了什么
填充只认同一条可确认估算：有 token 或请求上限，剩余比例是 `1 - 已用/上限` 且高于红线加既有余量，重置落在既有 2 小时时限内，同时没有其他 ready 的非 backlog 卡要用该渠道。无上限窗口即使 Remaining 为正也不当余量，临近重置也不能借用另一窗口的比例。未知额度的普通派发不变。
嵌套窗口里，一笔账本事件覆盖到的用量不算遗漏，置信度不因此下降。只有某个窗口仍放不进账本的用量才算遗漏。
design、plan、replan 的 prompt：节点没有单独事实时保留卡正文的 `## 事实摘要`，并去掉空的事实段；节点有开放问题时写入事实段，并从这次写出的卡正文去掉 `## 事实摘要`。已存储的卡不改。选择这条是因为 prompt.Build 对这三类总会留事实段，空段没有信息，而开放问题才是 §6.6 的单独事实，不能和正文再抄一遍。internal/prompt 未改。
同目标验收串行测试改用 queue_test.go 里已有的 shell 标记，不再调用 python3。区间仍不重叠，两张卡都会合并。

## 没做什么，以及原因
未改 internal/prompt、internal/loop、internal/quota、internal/model、internal/notify、cmd/cardex、design.md、goals.md。未改 windowFills，无上限窗口仍不填充。未改未知额度派发、排序或宿主信号。未新增硬门禁。S19 在 UpdateChannel 前写上 LimitTokens=100、UsedTokens=10，ResetAt 仍在 30 分钟内。

## 接口请求
S19 假渠道已有 token 上限和用量：5h LimitTokens=100、UsedTokens=10，ResetAt 仍在 30 分钟内。无上限的正 Remaining 仍不能当成可填充。本次 `go test ./...` 的 TestS19 通过。host 子命令、prompt.Build、notify.Stub、model.Budget 时长、Attempt 重置时间、runServe 改接 sched.Serve 仍见 R3-C.md。

## 已知问题
Ledger.Record 不能指定窗口。较短窗口已用量大于较长窗口能吸收的部分仍无法落点，调度把这部分标成不确定。
rebase 和 repair 缺少脚本时宿主仍会启动。e2e 的 scenarioDispose 仍按全文第一个 pass/rework 处置。

## 修复记录
无上限窗口不是填充余量，可确认估算要同时满足上限、比例和临近重置 → internal/sched/loop.go:805 → TestBacklogFillStoredRatioWithoutCap
重叠窗口被同一笔用量覆盖时不记为遗漏 → internal/sched/quota_seed.go:65 → TestSnapshotOverlapIsNotOmission
design/plan/replan 的 prompt 不留空事实段，也不把事实摘要写两遍 → internal/sched/loop.go:565 internal/sched/loop.go:653 → TestSchedulerPromptFacts
同目标验收区间不再用 python3 记录 → internal/merge/queue_test.go:645 → TestQueueSameTargetAcceptanceIntervalsDoNotOverlap
