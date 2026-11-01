# R4-C

## 做了什么

`scorecard.Compute` 按尝试的 route.model × 卡的 kind × tier 聚合。复核通过率记被复核卡。样本少于 5 写样本不足。没有 `schedules` 键时 `DefaultSchedule` 是每周 ranking，requires 含 `web`。`ScheduleCard` 把定时定义的 `Requires` 原样抄到卡上。`RankingMaterial` 替换来源并附定位表、渠道清单和成绩单摘要。`AbsorbHarvestedRanking` 对 `passed` 和 `result_unparsable` 宽松解析；违规、预算用尽、验收失败和已停止的卡不建提案。`FollowRanking` 在「批准」后调 `Approve`，不改已落库路由。打开数据库不入队、不吸收、不批准。

## 没做什么，以及原因

没有改 sched、prompt 拼装、question、harvest、e2e，也没有改 `design.go`。没有 `absorbDelegated`，没有给 `sched.New` 装 `Dispose`。没有抓榜，没有改定位表结构、路由或成绩单 CLI。

## 接口请求

- R3-C `internal/sched/loop.go`（本 lane 未改）：无 `schedules` 键时（732 行）调用 `loop.DefaultSchedule`（`ranking.go:41`）和 `loop.ApplyDefaultSchedule`（`ranking.go:67`）。`cardTemplate`（723 行）增加 `requires`，`enqueueTemplate`（813 行，建卡 843 行）抄到 `Card.Requires`。`promptText`（568 行）把 `r.Home` 传给 `loop.RankingMaterial`（`ranking.go:171`）。`deliver`（175 行）把真实收割的 outcome、error class 和结果原文（含 `result_unparsable`）传给 `loop.AbsorbHarvestedRanking`（`ranking.go:467`）。`sched.New`（65 行）保持 `Dispose` 为 nil。
- R3-D `internal/prompt/build.go`（本 lane 未改）：`Build`（30 行）对 ranking 发出 `RankingMaterial`。不要反向 import loop。
- R3-A `internal/loop/design.go`（本 lane 未改）：`absorbAttempt`（1527 行，默认分支 1608 行）把同一份真实收割结果（含 `result_unparsable`）传给 `loop.AbsorbHarvestedRanking`。不要加 ranking 分支，不要恢复 `absorbDelegated`。
- `question.Answer`（`service.go:62`，本 lane 未改）回答「批准」后调用 `loop.FollowRanking`（`ranking.go:407`）。question 不能 import loop。

## 转交

R4-D：不要给 `sched.New` 安装生产 `Dispose`，也不要把 TestS23 指到这个缺失钩子上。

## 已知问题

`schedules`、`cardTemplate`、`promptText`、`deliver`、`prompt.Build`、`absorbAttempt`、`question.Answer` 仍不调用上述函数。`ranking_sources` 仍会触发未知顶层键警告。dev 路径的复核吸收仍不写 `loop.review_pass`。R3-B `breaker.go` `knownTop`（183 行）尚未加入 `ranking_sources`。

## 修复记录

- 复核通过率记被复核卡的尝试模型 × 该卡 kind × tier，不记复核者的 review 桶 → `internal/scorecard/compute.go:64`、`internal/scorecard/compute.go:110` → `TestReviewPassFollowsReviewedCard`
- 没有 `schedules` 键时每周 ranking 的 requires 含 web，来源可覆盖，并附三项快照 → `internal/loop/ranking.go:41`、`internal/loop/ranking.go:171` → `TestRankingPromptSnapshots`
- 没有已知标题的提案由 `AbsorbRanking` 吸收成问题，批准后才发布且不改已落库尝试路由 → `internal/loop/ranking.go:545`、`internal/loop/ranking.go:407` → `TestRankingAbsorbLooseAndApprove`
- ranking 定时任务的能力要求覆盖每条定时定义，显式 requires 含 web 也抄到卡上 → `internal/loop/ranking.go:136` → `TestRankingScheduleRequires`
- 可宽松解析的格式差异不能在解析前被静默跳过（B6） → `internal/loop/ranking.go:528` → `TestRankingHarvestFormatStillAbsorbs`
- 定时入队和结果吸收不由打开数据库触发 → `internal/loop/ranking.go:67`、`internal/loop/ranking.go:467` → `TestRankingOpenDoesNotMaintain`
- ranking 的完成定义必须在调度、prompt 和批准入口成立（B1）；写域函数已覆盖，生产入口留在接口请求，本 lane 未改那些文件 → `internal/loop/ranking.go:41`、`internal/loop/ranking.go:136`、`internal/loop/ranking.go:171`、`internal/loop/ranking.go:407`、`docs/v2/lane-notes/R4-C.md:13` → `TestRankingScheduleRequires`、`TestRankingPromptSnapshots`、`TestRankingAbsorbLooseAndApprove`
- ranking 的吸收必须接在真实收割结果上并记下 outcome（B4）；deliver 与 absorbAttempt 仍由负责人绑定，不安装 Dispose，TestS23 不指向缺失钩子 → `internal/loop/ranking.go:467`、`internal/loop/ranking.go:511`、`docs/v2/lane-notes/R4-C.md:20` → `TestRankingHarvestFormatStillAbsorbs`、`TestRankingAbsorbHonorsHarvest`
