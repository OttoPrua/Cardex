# R4-C

## 做了什么

`scorecard.Compute` 按尝试的 route.model × 卡的 kind × tier 聚合 home 库。有尝试的桶：样本是尝试数；复核通过率是 distinct 卡里 entity 为 `card:<id>` 且类型为 `loop.review_pass` 的比例；合入率是 status 为 `merged` 的比例；平均返工是这些卡 rework 的均值（每卡一次）；每张合入卡的 token 是桶内已合入卡上的尝试用量之和除以 distinct 合入卡数，没有合入则为 0。样本少于 5 标样本不足。没有尝试的桶不出现。

ranking 模板要求能力 `web`。默认来源是 SWE-bench 官方榜、Aider 多语言编程榜、LMArena WebDev、LiveBench 编程类、Terminal-Bench、Artificial Analysis 编程指数。`config.json` 的 `ranking_sources` 字符串数组整表替换这六项。材料写明输入当前定位表、各渠道可用的模型清单、成绩单摘要，并要求 `.cardex-result.md` 的 JSON 提案。条目含 model、family、tiers、capabilities、preference、理由；不在当前表的模型另标新出现。

没有 `schedules` 键时 `loop.DefaultSchedule` 返回一条每周（168h）的 ranking 任务，requires 含 `web`。`AbsorbDisposed` 对 kind `ranking` 宽松解析提案：有条目则 `positioning.Propose`（不改已发布表）并建一个问题，正文含差异摘要和新出现模型。坏片段标待复核且不失败，合法字段保留。回答批准后 `FollowRanking` 调 `Approve`。已落库的尝试路由保持原渠道和模型。TestS23 不再 skip。

## 没做什么，以及原因

没有抓取榜单或打开网络。没有改定位行、路由选择、成绩单或定位 CLI、看板和其他包。没有改 `sched`，也没有改 `scenarioDispose` 或其他场景的 skip。`cardex scorecard` 与 `cardex positioning` 仍未实现。

## 接口请求

config 没有 `schedules` 时请入队 `loop.DefaultSchedule`，并给 `cardTemplate` 增加 Requires，否则 `web` 到不了派发。`sched.New` 的 Dispose 请调用 `loop.AbsorbDisposed(ctx, loop.Deps{Store: db}, attemptID, outcome, resultText)`，不要把解析抄进 sched；deliver 之后用同一指针 `UpdateCard`，吸收改过的卡要写回该指针。ranking 的 `prompt.Build` 请改用 `loop.RankingMaterial(home)`。请把 `ranking_sources` 加入 `knownTop`，免得 `LoadConfig` 警告。

## 已知问题

无 `schedules` 的临时 home 上 `sched.New().Tick` 成功，但一张卡都不入队，每周 ranking 卡不会出现。Dispose 仍是 nil，生产 Tick 不会吸收 ranking。纯 JSON 回执没有已知标题时收割为 `result_unparsable`，吸收仍读 ResultText。`ranking_sources` 会触发未知顶层键警告。
