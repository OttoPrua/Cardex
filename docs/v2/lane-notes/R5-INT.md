# R5-INT

## 做了什么

`git merge --no-ff v2-R4-D`。`Runner` 同时留下 ranking 的 `rankAnswer` 和 `Classify`。`New` 只安装「批准」钩子，`Classify` 保持 nil。`internal/e2e` 里两份相同的 `questionsOf` 只留 `s25_flow_test.go`。发信号仍只走 `internal/host/signal.go`，扫描没有白名单。ranking 的 transport 和 timeout 不再进入开发卡的退避等待。周 ranking 卡 `sch-ranking-` 不挡住目标完成。

## 没做什么，以及原因

没有改 `goals.md` 或 `design.md`，没有合并或改名 `auto_route` 与 `pin_released`，没有改默认退避时长，也没有放宽断言。`TestFaults` 和默认退避不能同时成立。

## 接口请求

N2：`absorbReview`（`internal/loop/dev_apply.go:783`）把父卡排进 merge，不写 `loop.review_pass`。`absorbDevReview`（`internal/loop/design.go:283`）只在 `card.Node` 非空时写该事件。本 lane 不改这两处。

## 已知问题

`auto_route`（`internal/loop/dev_apply.go:358`）和 `pin_released`（`internal/sched/launch_route.go:369`）都保留，跟进项，不合并。
`TestFaults` 在 `internal/e2e/faults_test.go:101` 要求 90 秒内 transport 两次失败后再合并。`TestFollowupTransportBackoff` 在 `internal/loop/dev_followup_test.go:135` 要求默认先等 30 秒，在 `internal/loop/dev_followup_test.go:146` 要求第二次等到首次之后 90 秒。成功尝试赶不上这个窗口。两边都没改。

## 修复记录

合并 v2-R4-D 时双方都留：Runner 同时有 ranking 钩子和 Classify，New 只默认批准钩子；相同 questionsOf 只留一份；信号只走 host → internal/sched/loop.go:67、internal/sched/loop.go:70、internal/sched/loop.go:79、internal/e2e/s25_flow_test.go:460、internal/host/signal.go:18 → TestNewKeepsRankingHookAndNilClassify、TestSignalGuard
ranking 收割复用失败决策，但 transport 与 timeout 必须落到已结束状态，不能停在开发卡的退避等待 → internal/loop/dev_apply.go:315 → TestRankingHarvestDisposition、TestFollowupTransportBackoff
没有 schedules 时仍入队带 web 的周 ranking；这张 `sch-ranking-` 卡不算目标未完成，完成问题仍可出现 → internal/loop/dev_apply.go:1575、internal/loop/ranking.go:61 → TestS25、TestRankingScheduleTick、TestS23
build、vet、test 必须全绿；`TestFaults` 与默认退避不能同时成立，这两处断言未改 → internal/e2e/faults_test.go:101、internal/loop/dev_followup_test.go:135、internal/loop/dev_followup_test.go:146 → TestFaults、TestFollowupTransportBackoff
