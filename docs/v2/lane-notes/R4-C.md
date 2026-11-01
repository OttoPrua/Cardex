# R4-C

## 做了什么

`scorecard.Compute` 按尝试的 route.model × 卡的 kind × tier 聚合。复核通过率记被复核卡：非复核卡用本卡；复核卡先用已有依赖，否则用 `loop.review_bind` 的 design。通过事件的 data 不读。同一被复核卡多次通过只计一次。合入率、平均返工、每张合入卡 token、样本数、样本不足（少于 5）不变。

没有 `schedules` 键时 `DefaultSchedule` 是每周 ranking，requires 含 `web`；有该键则没有默认任务。`RankingMaterial` 用来源列表替换模板里的六项默认榜，并附上当前定位表、各渠道模型清单和成绩单摘要（样本少于 5 写样本不足）。`absorbDelegated` 宽松解析没有已知标题的提案（含畸形正文），建立定位提案和问题。回答批准后 `FollowRanking` 调 `Approve`，不改已落库尝试路由。生产入口还没有调用这些函数。

## 没做什么，以及原因

没有改 sched、prompt 拼装、question、e2e，也没有留下 `design.go` 的改动。没有抓榜，没有改定位表结构、路由、成绩单 CLI 或看板。

## 接口请求

- R3-C `internal/sched/loop.go`：config 没有 `schedules` 键时入队 `loop.DefaultSchedule`（每周 ranking）。`schedules`（732 行）现在读不到该键就返回。`enqueueTemplate`（843 行）把 `Requires`（含 `web`）抄到入队卡上；`cardTemplate`（723 行）还没有这个字段。`promptText`（568 行）对 ranking 使用 `loop.RankingMaterial(home)`，来源覆盖和三项快照才是实际 prompt。`prompt` 已被 loop 引用，`Build` 不要反向 import loop。`deliver`（175 行）在 `Dispose` 为 nil 时不吸收；默认收割应调用 `loop.AbsorbDisposed`，成功后用同一 `*Card` 指针 `UpdateCard`。
- R3-D `internal/prompt/build.go`：`Build`（34 行）拼出的 ranking 正文必须是上述材料，而不是只写“请读取”的前言。
- R3-A `internal/loop/design.go`：`absorbAttempt` 的 switch 不要加 ranking 分支，也不要把解析抄进 design。default（1608 行）要调用 `absorbDelegated`（`ranking.go:340`）。本 lane 已拆出之前加在 default 上的委托。
- R3-B：复核 pass 仍不写可追随的 `loop.review_pass`，请在复核卡上追加该事件。`breaker.go` `knownTop`（183 行）加入 `ranking_sources`。
- 定位问题经生产路径回答“批准”后调用已有的 `loop.FollowRanking`（`ranking.go:292`）。`question.Answer` 目前只保存答案。

## 转交

R4-D `internal/e2e` TestS23：走缺省 `schedules` 下的默认每周 ranking，经正常收割和问题回答“批准”之后，新卡的路由使用新定位。不要手工建 ranking 卡，不要注入 Dispose，不要直接调用 `FollowRanking`。

## 已知问题

无 `schedules` 时生产 Tick 仍不入队每周 ranking。`Dispose` 为 nil 时生产收割不吸收 ranking。回答批准后没有生产调用 `FollowRanking`。`ranking_sources` 仍会触发未知顶层键警告。dev 路径的复核吸收仍不写 `loop.review_pass`。现有 TestS23 仍手工建卡、注入 Dispose 并直接调用 `FollowRanking`，拆出 default 委托后等不到问题。本轮不恢复该委托，也不改 e2e、sched、question；全量测试因此只在 TestS23 失败，要等 R3-A 在 default 调用 `absorbDelegated`、R4-D 把 TestS23 改到默认定时、正常收割和批准回答之后才能变绿。

## 修复记录

- 复核通过率记被复核卡的尝试模型 × 该卡 kind × tier，不记复核者的 review 桶 → `internal/scorecard/compute.go:64`、`internal/scorecard/compute.go:110` → `TestReviewPassFollowsReviewedCard`
- ranking 完成必须由默认生产入口执行；写域只保留每周任务、来源覆盖和三项快照，辅助函数不算已经接线 → `internal/loop/ranking.go:40`、`internal/loop/ranking.go:56` → `TestRankingPromptSnapshots`
- 写域外吸收入口不由本 lane 修改；没有已知标题的提案仍由 ranking 吸收成问题，批准后才发布且不改已落库尝试路由 → `internal/loop/ranking.go:340`、`internal/loop/ranking.go:292` → `TestRankingAbsorbLooseAndApprove`
