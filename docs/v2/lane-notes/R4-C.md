# R4-C

## 做了什么

`scorecard.Compute` 按尝试的 route.model × 卡的 kind × tier 聚合。复核通过率：`loop.review_pass` 在非复核卡上记该卡；在复核卡上，被复核卡先取该卡已存在的依赖，否则取 `loop.review_bind` 的 design（指向被复核卡）。通过事件的 data 不读，它写的是复核卡自身。同一被复核卡多次通过只计一次。合入率、平均返工、每张合入卡 token、样本数、样本不足（少于 5）不变。

`RankingMaterial` 在来源清单后附上该 home 的当前定位表、各渠道模型清单和成绩单摘要。无 `ranking_sources` 时来源是模板里的六项默认榜；有该数组则整表替换。无 `schedules` 键时 `DefaultSchedule` 仍是每周 ranking，requires 含 `web`。`absorbRanking` 仍宽松解析提案、建问题；回答批准后 `FollowRanking` 调 `Approve`，不改已落库尝试路由。

## 没做什么，以及原因

没有改 sched、prompt 拼装、question、e2e。没有抓榜，没有改定位表结构、路由、成绩单 CLI 或看板。dev 路径的复核吸收仍不写 `loop.review_pass`，本 lane 不在写域外补事件。

## 接口请求

- R3-C `internal/sched`：config 没有 `schedules` 键时入队 `loop.DefaultSchedule`（每周 ranking）。`enqueueTemplate` 把 `Requires`（含 `web`）抄到入队卡上。`Runner.promptText` 对 ranking 使用 `loop.RankingMaterial(home)`，来源覆盖和三项输入才是实际写出的 prompt。`prompt` 已被 loop 引用，`Build` 不要反向 import loop。默认收割的 Dispose 调用 `loop.AbsorbDisposed`，成功后用同一 `*Card` 指针 `UpdateCard` 写回。
- R3-D `internal/prompt`：ranking 卡实际 prompt 必须是上述材料，而不是只写“请读取”的前言。
- R3-A `internal/loop/design.go` `absorbAttempt`：kind 为 `ranking` 时调用已有的 `Deps.absorbRanking`。本 lane 已从 `design.go:1608` 的 default 前拆掉原先的 ranking 分支。
- R3-B `internal/loop/dev_apply.go` `absorbReview`：pass 只改父卡状态，不写可追随的 `loop.review_pass`。请在复核卡上追加该事件；被复核卡仍由依赖，或由 `loop.review_bind`（design 为被复核卡，不是复核卡）指向。`absorbDevReview` 仅在复核卡有 node 且 `## 复核结论` 为 pass 时写事件，其余通过也没有可追随事件，请一并补上。`breaker.go` `knownTop` 加入 `ranking_sources`。
- 定位问题经生产路径回答“批准”后调用已有的 `loop.FollowRanking`。

## 转交

R4-D `internal/e2e` TestS23：走缺省 `schedules` 下的默认每周 ranking。不要注入 Dispose，也不要自己调用 `FollowRanking`。问题按生产路径回答“批准”之后，后续卡经正常推进采用新定位。

## 已知问题

无 `schedules` 时生产 Tick 仍不入队每周 ranking。Dispose 仍为 nil 时生产收割不吸收 ranking。`ranking_sources` 仍会触发未知顶层键警告。design 吸收未接回 `absorbRanking` 前，TestS23 经 `AbsorbDisposed` 收不到提案问题。

## 修复记录

- 复核通过率记被复核卡的尝试模型 × 该卡 kind × tier，不记复核者的 review 桶 → `internal/scorecard/compute.go:64`、`internal/scorecard/compute.go:110` → `TestReviewPassFollowsReviewedCard`
- ranking 材料带上有效来源以及当前定位表、渠道模型清单、成绩单摘要；写域外接线只登记请求 → `internal/loop/ranking.go:56`、`internal/loop/ranking.go:81` → `TestRankingPromptSnapshots`
- 写域外的 ranking 吸收分派拆出，改由 ranking 实现承接 → `internal/loop/design.go:1608`、`internal/loop/ranking.go:338` → `TestRankingAbsorbLooseAndApprove`
