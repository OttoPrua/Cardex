# R4-C-F1

## 做了什么

没有 `schedules` 键时，调度 tick 入队默认每周 ranking，requires 含 `web`；显式 `card_template.requires` 原样抄上卡。ranking 的 prompt 由 `prompt.Build` 拼装，材料由 sched 传入。`loop.Dispose` 把 ranking 收割交给 `AbsorbHarvestedRanking`。`sched.New` 在回答「批准」后调用 `FollowRanking`。同一尝试或同一份提案不重复吸收；已有定位问题不再让后一次尝试直接完成。S23 只走 tick、Dispose 和挂上钩子的回答。

## 没做什么，以及原因

没有改 `design.go`、`dev_apply.go` 的判定，没有恢复 `absorbDelegated`，没有给 `absorbAttempt` 加 ranking 分支，没有改 `cmd/`、定位表形状、`goals.md` 或 `design.md`。N2 的生产者不在写域。

## 接口请求

- N2：没有 node 的开发复核 `pass` 在 `internal/loop/dev_apply.go:606` 的 `absorbReview` 里把父卡排进 merge，但不写 `loop.review_pass`。设计侧 `internal/loop/design.go:283` 的 `absorbDevReview` 只在 `card.Node != ""` 时写该事件，node 为空就跳过。成绩单只统计已经写下的事件。请这两个写域补上通过事件。
- `cmd/cardex` 用 `question.New` 回答，不经过 `sched.New` 注册的钩子。单独跑 CLI 的进程不会在「批准」后调用 `FollowRanking`。请 cmd 复用这条钩子。

## 已知问题

`ranking_sources` 仍可能触发未知顶层键警告。`breaker.go` 的 `knownTop` 还没有这个键。本轮不修。

## 修复记录

- ranking 的定时、prompt 和批准必须走正常入口：没有 schedules 时入队每周 ranking 且 requires 含 web，显式 requires 原样入卡；prompt 经 Build 带上调用方材料；回答「批准」后批准提案且不改已落库路由（B1） → internal/sched/loop.go:75、internal/sched/loop.go:544、internal/sched/loop.go:795、internal/sched/loop.go:936、internal/prompt/build.go:38、internal/question/service.go:33 → TestRankingScheduleTick、TestRankingPromptFromBuild、TestRankingApproveHook、TestRankingDefaultEntry、TestS23
- ranking 必须经正常收割自动生成提案和问题，S23 验证这条闭环；同一尝试再吸收不新增（B4） → internal/loop/dispose.go:129、internal/loop/dispose.go:138 → TestDisposeRanking、TestDeliverDoesNotAbsorbRanking、TestS23
- 历史定位问题不能让新尝试跳过解析并直接完成，按尝试和提案判重（N1） → internal/loop/ranking.go:577、internal/loop/ranking.go:619 → TestDisposeRanking
- 生产认可的复核通过应产生成绩单通过事件；写域内没有可改的位置（N2） → 无 → 见接口请求
