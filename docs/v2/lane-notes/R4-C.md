# R4-C

## 做了什么

`scorecard.Compute` 按尝试的 route.model × 卡的 kind × tier 聚合。复核通过率记被复核卡。样本少于 5 写样本不足。没有 `schedules` 键时 `DefaultSchedule` 是每周 ranking，requires 含 `web`。`ScheduleCard` 把定时定义的 `Requires` 原样抄到卡上。`RankingMaterial` 替换来源并附定位表、渠道清单和成绩单摘要。`AbsorbHarvestedRanking` 对 `passed` 和 `result_unparsable` 宽松解析；违规、预算用尽、验收失败和已停止的卡不建提案。同一 attempt 再吸收不新增提案或问题。`FollowRanking` 在「批准」后调 `Approve`，不改已落库路由。打开数据库不入队、不吸收、不批准。

## 没做什么，以及原因

没有改 sched、prompt 拼装、question、harvest、e2e，也没有改 `design.go`。没有 `absorbDelegated`，没有给 `sched.New` 装 `Dispose`。没有抓榜，没有改定位表结构、路由或成绩单 CLI。

## 接口请求

- R3-C `internal/sched/loop.go`（本 lane 未改；这些入口现在都不调用下列函数）：无 `schedules` 键时，请在 `schedules`（732 行）调用 `loop.DefaultSchedule`（`ranking.go:41`）和 `loop.ApplyDefaultSchedule`（`ranking.go:67`），让默认每周 ranking 卡的 requires 含 `web`。请给 `cardTemplate`（723 行）增加 `requires`，并在 `enqueueTemplate`（813 行，建卡 843 行）把显式列表原样抄到 `Card.Requires`。请让 `promptText`（568 行）把 `r.Home` 传给 `loop.RankingMaterial`（`ranking.go:171`）。请让 `deliver`（175 行）把真实收割的 outcome、error class、验收结果和结果原文（含 `result_unparsable`）传给 `loop.AbsorbHarvestedRanking`（`ranking.go:467`）。`sched.New`（65 行）保持 `Dispose` 为 nil。在 `schedules`、`enqueueTemplate`、`promptText` 和 `deliver` 上核对：显式 `requires` 含 `web` 被抄上卡；没有 `schedules` 键时入队每周 ranking，已有 `schedules` 时不入队；同一 attempt 再吸收不新增提案或问题。
- R3-D `internal/prompt/build.go`（本 lane 未改；`Build` 现在不附 ranking 材料）：请让 ranking 的 attempt prompt 带上 `loop.RankingMaterial`（`ranking.go:171`）。材料是固定来源清单（配置清单整份替换）加上当前定位表、各渠道模型和成绩单摘要。`Build`（30 行）不要反向 import loop；sched 的 `promptText` 可以调用它。在 `Build` 的 ranking 产物上核对这四项。
- R3-A `internal/loop/design.go`（本 lane 未改；默认分支现在不吸收 ranking）：请让 `absorbAttempt`（1527 行，默认分支 1608 行）把同一份真实收割的 outcome、error class、验收结果和结果原文（含 `result_unparsable`）传给 `loop.AbsorbHarvestedRanking`（`ranking.go:467`）。不要加 ranking 分支，不要恢复 `absorbDelegated`，不要安装 `Dispose`。在该默认路径核对：违规、预算用尽、验收失败和已停止的卡不建提案，同一 attempt 再吸收不新增提案或问题。
- `question.Answer`（`service.go:62`，76 行只保存回答；本 lane 未改）：请在回答「批准」后调用 `loop.FollowRanking`（`ranking.go:407`）。在该入口核对提案被批准且已落库尝试路由不变。question 不能 import loop，由该文件的负责人接上。

## 转交

R4-D：S23（`internal/e2e/scenarios_test.go:941` 手建已结束 attempt，947 行直接吸收，975 行再次调用辅助函数，991 行回答后 994 行才 `FollowRanking`）的派发 → fake 执行 → 收割 → 批准 → 新卡路由，以及重复 tick 不重复消费，等 R3-C `deliver`、R3-A `absorbAttempt` 和 question 负责人接通。不要给 `sched.New` 安装生产 `Dispose`，TestS23 不得调用这个为 nil 的 `Dispose` 钩子。

## 已知问题

`schedules`、`cardTemplate`、`promptText`、`deliver`、`prompt.Build`、`absorbAttempt`、`question.Answer` 仍不调用上述函数。`ranking_sources` 仍会触发未知顶层键警告。dev 路径的复核吸收仍不写 `loop.review_pass`。R3-B `breaker.go` `knownTop`（183 行）尚未加入 `ranking_sources`。

## 修复记录

- 显式 Requires（含 web）原样抄到定时卡；缺少 config.json 或没有 schedules 键时入队每周 ranking 且 requires 含 web，已有 schedules 键时不入队；ranking_sources 整份替换默认来源，并附当前定位表、各渠道模型和成绩单摘要；回答「批准」后批准提案且不改已落库尝试路由；打开数据库不入队、不吸收、不批准（B1） → internal/loop/ranking.go:41、internal/loop/ranking.go:67、internal/loop/ranking.go:136、internal/loop/ranking.go:171、internal/loop/ranking.go:407 → TestRankingScheduleRequires、TestRankingPromptSnapshots、TestRankingAbsorbLooseAndApprove、TestRankingOpenDoesNotMaintain
- passed 与 result_unparsable 的收割元组（outcome、错误类、验收结果、结果原文）宽松解析成一份提案和一条写明差异并标出新模型的问题，畸形提案不硬拒绝；违规、预算用尽、验收失败和已停止的卡不建提案；同一 attempt 再吸收不新增提案或问题（B4） → internal/loop/ranking.go:467、internal/loop/ranking.go:494、internal/loop/ranking.go:519、internal/loop/ranking.go:528、internal/loop/ranking.go:629、internal/loop/ranking.go:783 → TestRankingHarvestFormatStillAbsorbs、TestRankingAbsorbHonorsHarvest、TestRankingAbsorbLooseAndApprove
