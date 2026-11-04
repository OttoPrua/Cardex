# R5-INT

## 做了什么

`git merge --no-ff v2-R4-D`。`Runner` 同时留下 ranking 的 `rankAnswer`（`internal/sched/loop.go:67`）和 `Classify`（`internal/sched/loop.go:70`）。`New` 只安装「批准」钩子（`internal/sched/loop.go:79`），`Classify` 保持 nil。`internal/e2e` 里两份相同的 `questionsOf` 只留 `internal/e2e/s25_flow_test.go:460`。发信号仍只走 `internal/host/signal.go:18`，扫描没有白名单。ranking 的 transport 和 timeout 不再进入开发卡的退避等待（`internal/loop/dev_apply.go:315`）。周 ranking 卡 `sch-ranking-` 不挡住目标完成（`internal/loop/dev_apply.go:1575`）。

`TestFaults` 在 tick 前把 `loop.retry_backoff_seconds` 写成 1，文件是 `$CARDEX_HOME/config.json`，即 `loop.LoadConfig` 已读的那份。c4 仍走同渠道两次等待，dispatch 到期释放，第三次尝试不早于第二次截止。默认退避仍是 30 秒。90 秒期限和 `assertTransport` 的次数、渠道检查保持原样。周 ranking 卡保持 ready，额外 tick 不改变它的状态和事件。

## 没做什么，以及原因

没有改 `goals.md`、`design.md`、默认退避或 `TestFollowupTransportBackoff`，没有给信号扫描加白名单。

写域外未改：ranking 定时、派发和回调接线（`internal/sched/loop.go:67`）；dispatch 释放到期 `backoff:`（`internal/sched/backoff.go:14`）；处置换渠道写回卡并清空卡的 Model（`internal/loop/dev_apply.go:352`）；`merge_success`（`internal/merge/queue.go:164`）；`launchCard` 的 classify（`internal/sched/classify.go:19`）和显式指定路由（`internal/sched/launch_route.go:32`）。`auto_route` 与 `pin_released` 都保留。复核吸收缺口见接口请求。

## 接口请求

N2：`absorbReview`（`internal/loop/dev_apply.go:783`）把父卡排进 merge，不写 `loop.review_pass`。`absorbDevReview`（`internal/loop/design.go:283`）只在 `card.Node` 非空时写该事件。本 lane 不改这两处。

## 已知问题

`auto_route`（`internal/loop/dev_apply.go:358`）和 `pin_released`（`internal/sched/launch_route.go:369`）都保留，跟进项，不合并。

## 修复记录

故障套件沿生产配置把同渠道退避基数设为 1 秒，两次等待后第三次尝试不早于第二次截止，默认 30 秒和 90 秒期限不变 → internal/e2e/faults_test.go:47、internal/e2e/faults_test.go:274、internal/e2e/faults_test.go:281、internal/e2e/faults_test.go:477 → TestFaults
