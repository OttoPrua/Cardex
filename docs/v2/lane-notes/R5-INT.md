# R5-INT

## 做了什么

`git merge --no-ff v2-R4-D`。`Runner` 同时留下 ranking 的 `rankAnswer` 和 `Classify`。`New` 只安装「批准」钩子，`Classify` 保持 nil。`internal/e2e` 里两份相同的 `questionsOf` 只留 `s25_flow_test.go`。发信号仍只走 `internal/host/signal.go`，扫描没有白名单。

## 没做什么，以及原因

没有改 `goals.md` 或 `design.md`，没有合并或改名 `auto_route` 与 `pin_released`，没有改退避、ranking 入队或处置，也没有放宽断言。三对断言不能同时成立，套件停在红。

## 接口请求

N2：`absorbReview`（`internal/loop/dev_apply.go:782`）把父卡排进 merge，不写 `loop.review_pass`。`absorbDevReview`（`internal/loop/design.go:283`）只在 `card.Node` 非空时写该事件。本 lane 不改这两处。

## 已知问题

`auto_route`（`internal/loop/dev_apply.go:357`）和 `pin_released`（`internal/sched/launch_route.go:369`）都保留，跟进项，不合并。
以下断言冲突，两边都没改：
- `TestFaults` 在 `internal/e2e/faults_test.go:101` 要求 90 秒内 transport 两次失败后再合并。`TestFollowupTransportBackoff` 在 `internal/loop/dev_followup_test.go:135` 要求默认先等 30 秒，在 `internal/loop/dev_followup_test.go:146` 要求第二次等到首次之后 90 秒。成功尝试赶不上这个窗口。
- `TestS25` 的 close 在 `internal/e2e/s25_flow_test.go:125` 依赖完成问题。`anyUnfinished`（`internal/loop/dev_apply.go:1570`）把未结束的卡算未完成。没有 schedules 时默认 ranking 卡入活动目标且 requires 含 web（`internal/sched/ranking_entry_test.go:27`）。S25 的路由没有 web（`internal/e2e/scenarios_test.go:2021`），该卡停在 ready。
- `TestRankingHarvestDisposition` 的 transport 与 timeout 在 `internal/loop/dispose_ranking_test.go:180` 不许 waiting。默认退避 30 秒（`internal/loop/breaker.go:42`）经 `internal/loop/dev_apply.go:314` 把这两种结果写成 waiting。开发卡的退避测试仍要求这段等待。

## 修复记录

合并 v2-R4-D 时双方都留：Runner 同时有 ranking 钩子和 Classify，New 只默认批准钩子；相同 questionsOf 只留一份；信号只走 host → internal/sched/loop.go:67、internal/sched/loop.go:70、internal/sched/loop.go:79、internal/e2e/s25_flow_test.go:460、internal/host/signal.go:18 → TestNewKeepsRankingHookAndNilClassify、TestSignalGuard
build、vet、test 必须全绿且已提交；三对断言不能同时成立，断言未改，套件未绿 → internal/e2e/faults_test.go:101、internal/loop/dev_followup_test.go:135、internal/e2e/s25_flow_test.go:125、internal/loop/dev_apply.go:1570、internal/loop/dispose_ranking_test.go:180、internal/loop/dev_apply.go:314 → TestFaults、TestFollowupTransportBackoff、TestS25、TestRankingScheduleTick、TestRankingHarvestDisposition
