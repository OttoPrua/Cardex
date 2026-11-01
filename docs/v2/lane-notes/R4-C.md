# R4-C

## 做了什么

`scorecard.Compute` 按尝试的 route.model × 卡的 kind × tier 聚合。复核通过率记被复核卡：非复核卡用本卡；复核卡先用已有依赖，否则用 `loop.review_bind` 的 design。通过事件的 data 不读。同一被复核卡多次通过只计一次。合入率、平均返工、每张合入卡 token、样本数、样本不足（少于 5）不变。

没有 `schedules` 键时 `DefaultSchedule` 是每周 ranking，requires 含 `web`；有该键则没有默认任务。`RankingMaterial` 用来源列表替换模板里的六项默认榜，并附上当前定位表、各渠道模型清单和成绩单摘要（样本少于 5 写样本不足）。`AbsorbRanking` 宽松解析没有已知标题的提案，建立定位提案和问题。回答批准后 `FollowRanking` 调 `Approve`，不改已落库尝试路由。`InstallRankingProduction`（`ranking_prod.go:58`）把这些接到 sched 已有的 `devStep` 与 `buildPrompt`，并在打开 `cardex.db` 时做同样的维护。`sched.New` 仍不安装 `Dispose`。

## 没做什么，以及原因

没有改 sched、prompt 拼装、question，也没有改 `design.go`。没有 `absorbDelegated`。没有抓榜，没有改定位表结构、路由、成绩单 CLI 或看板。

## 接口请求

- R3-C `internal/sched/loop.go`：`schedules`（732 行）在没有 `schedules` 键时仍直接返回。最小改动是调用 `loop.DefaultSchedule`（`ranking.go:40`）。`enqueueTemplate`（813 行）把 `Requires`（含 `web`）抄到卡上；`cardTemplate`（723 行）还没有这个字段。`promptText`（568 行）把 `r.Home` 传给 `loop.RankingMaterial`（`ranking.go:66`）。`deliver`（175 行）在 `Dispose` 为 nil 时仍不吸收；收割成功后对同一 `*Card` 调用 `loop.AbsorbRanking`（`ranking.go:350`）。`sched.New`（65 行）继续保持 `Dispose` 为 nil。
- R3-D `internal/prompt/build.go`：`Build`（30 行）对 ranking 应发出上述材料。`prompt` 已被 loop 引用，`Build` 不要反向 import loop。
- R3-A `internal/loop/design.go`：`absorbAttempt`（1527 行，默认分支 1608 行）由 loop 负责人调用 `loop.AbsorbRanking`。不要加 ranking 分支，不要恢复 `absorbDelegated`。本 lane 不改 `design.go`。
- R3-B：复核 pass 仍不写 `loop.review_pass`。`breaker.go` `knownTop`（183 行）加入 `ranking_sources`。这两项不在本次发现里。
- `question.Answer`（`service.go:62`）仍只保存答案。回答「批准」后应调用 `loop.FollowRanking`（`ranking.go:302`）。question 不能 import loop。

## 转交

R4-D `internal/e2e` TestS23 已改走 `sched.New`（`Dispose` 保持 nil）、缺省每周 ranking、收割后的提案问题，以及 `question.Answer` 的「批准」。新卡路由用新定位，已落库尝试路由不变。不要再手建 ranking 卡、注入 `Dispose`，或直接调用 `AbsorbRanking` / `FollowRanking`。

## 已知问题

`schedules`、`deliver`、`prompt.Build`、`absorbAttempt`、`question.Answer` 的源码仍不调用上述辅助函数；现行为挂在 `ranking_prod.go` 的协作者和连接钩子上。`ranking_sources` 仍会触发未知顶层键警告。dev 路径的复核吸收仍不写 `loop.review_pass`。

## 修复记录

- 复核通过率记被复核卡的尝试模型 × 该卡 kind × tier，不记复核者的 review 桶 → `internal/scorecard/compute.go:64`、`internal/scorecard/compute.go:110` → `TestReviewPassFollowsReviewedCard`
- ranking 完成必须由默认生产入口执行；写域只保留每周任务、来源覆盖和三项快照 → `internal/loop/ranking.go:40`、`internal/loop/ranking.go:66` → `TestRankingPromptSnapshots`
- 没有已知标题的提案由 `AbsorbRanking` 吸收成问题，批准后才发布且不改已落库尝试路由 → `internal/loop/ranking.go:350`、`internal/loop/ranking.go:302` → `TestRankingAbsorbLooseAndApprove`
- 默认入口才算完成 ranking：无 schedules 时入队 requires 含 web 的每周卡，prompt 带来源和三项快照，nil Dispose 的收割变成提案问题，批准后新卡用新定位且已落库路由不变 → `internal/loop/ranking_prod.go:58`、`internal/loop/ranking.go:40`、`internal/loop/ranking.go:66`、`internal/loop/ranking.go:302`、`internal/loop/ranking.go:350` → `TestRankingDefaultEntry`、`TestS23`
