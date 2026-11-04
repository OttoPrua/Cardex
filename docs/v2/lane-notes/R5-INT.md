# R5-INT

## 做了什么

`git merge --no-ff v2-R4-D`。`Runner` 同时留下 ranking 的 `rankAnswer`（`internal/sched/loop.go:67`）和 `Classify`（`internal/sched/loop.go:70`）。`New` 只安装「批准」钩子（`internal/sched/loop.go:79`），`Classify` 保持 nil。`internal/e2e` 里两份相同的 `questionsOf` 只留 `internal/e2e/s25_flow_test.go:460`。发信号仍只走 `internal/host/signal.go:18`，扫描没有白名单。ranking 的 transport 和 timeout 不再进入开发卡的退避等待（`internal/loop/dev_apply.go:315`）。周 ranking 卡 `sch-ranking-` 不挡住目标完成（`internal/loop/dev_apply.go:1575`）。

## 没做什么，以及原因

没有改 `goals.md` 或 `design.md`，没有合并或改名 `auto_route` 与 `pin_released`，没有改默认退避时长，也没有放宽断言。`TestFaults` 和 `TestFollowupTransportBackoff` 不能同时成立。

## 接口请求

N2：`absorbReview`（`internal/loop/dev_apply.go:783`）把父卡排进 merge，不写 `loop.review_pass`。`absorbDevReview`（`internal/loop/design.go:283`）只在 `card.Node` 非空时写该事件。本 lane 不改这两处。

## 已知问题

`auto_route`（`internal/loop/dev_apply.go:358`）和 `pin_released`（`internal/sched/launch_route.go:369`）都保留，跟进项，不合并。
`TestFaults` 在 `internal/e2e/faults_test.go:97` 要求批次里其余卡已合并，并在 `internal/e2e/faults_test.go:102` 把窗口定在 90 秒。`TestFollowupTransportBackoff` 在 `internal/loop/dev_followup_test.go:135` 要求默认先等 30 秒，在 `internal/loop/dev_followup_test.go:146` 要求第二次等到首次之后 90 秒。成功尝试赶不上这个窗口。两边都没改。

## 修复记录

未放宽断言：`TestFaults` 要在 90 秒内于两次 transport 失败后合并，`TestFollowupTransportBackoff` 要默认先等 30 秒、再等到首次之后 90 秒，两者不能同时成立 → internal/e2e/faults_test.go:97、internal/e2e/faults_test.go:102、internal/loop/dev_followup_test.go:135、internal/loop/dev_followup_test.go:146 → TestFaults、TestFollowupTransportBackoff
