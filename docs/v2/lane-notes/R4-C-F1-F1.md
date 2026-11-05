# R4-C-F1-F1

## 做了什么
同卡 ranking 的新尝试不再只因本卡已有相同条目的问题和提案就结束。旧提案已是 approved 或 rejected，且当前定位表与这些条目不同时，吸收另写一份未终结提案，并开一个未回答、可选「批准」的问题。同一尝试已有 outcome 时再吸收不新增。定位仍与该提案一致时，这次新尝试不另建。另一张卡的相同条目仍各自提案。

## 没做什么，以及原因
没有改 `internal/loop/design.go` 或 `internal/loop/dev_apply.go`，没有让成绩单用 `merge_queued` 代替 `loop.review_pass`。N2 的通过事件不在本写域。没有恢复 `absorbDelegated`，没有改 `absorbAttempt`、`cmd/`、定位表形状、`goals.md` 或 `design.md`。

## 接口请求
N2：没有 node 的开发复核 `pass` 在 `internal/loop/dev_apply.go:783` 的 `absorbReview` 把父卡改为 `merge_queued`，不写 `loop.review_pass`。设计复核 `internal/loop/design.go:284` 的 `absorbDevReview` 只在 `card.Node != ""` 时写该事件。成绩单 `internal/scorecard/compute.go:110` 的 `passedCards` 只计真实 `loop.review_pass`。请这两个写域补上通过事件，包括 node 为空的开发复核，并加漏计回归。

## 已知问题
`ranking_sources` 仍可能触发未知顶层键警告。`internal/loop/breaker.go:188` 的 `knownTop` 还没有这个键。本轮不修。

## 修复记录
同卡新尝试不能只凭历史相同条目认定已处理；旧提案已终结且当前定位不同则另建提案和问题 → internal/loop/ranking.go:659 internal/loop/ranking.go:667 → TestRankingDedup
生产认可的复核通过应产生成绩单通过事件；写域内没有改 absorbReview 与 absorbDevReview → internal/loop/dev_apply.go:783 internal/loop/design.go:284 → 见接口请求
