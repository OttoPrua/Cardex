# R3-B-F1

## 做了什么

同一条 `decideHarvest` 处置开发收割。`transport` / `timeout` 在 `retry_backoff_seconds` 大于 0 时写入 `retry_backoff`，卡在截止前保持 `waiting`；到点后的 `DevStep` 才把同一渠道放回 ready，次数仍受 `transport_retries` 限制，用尽后换渠道。默认 0，截止就是当前时刻，状态仍是 ready。复核 family 取成功尝试的模型在定位表里的 family，没有该行才用渠道 family；推出来的 family 非空且与卡上旧值不同就覆盖。整理结果为 violation 时作废该次尝试并派问题，原卡保持等待复核。一张清单里每个剩余的环各断一条边、各挂一个问题。清单点名的节点不存在时，问题挂到当前节点，正文仍写出原节点，本轮不回滚。auth、breaker、budget、budget_reminder、done 投递失败后，下一次 `DevStep` 按 `notice_due` 再投；成功写入 `notice_delivered`，不再重发。无进展熔断的锚点是该节点最新的 `merge_success`，没有这条事件时用第一次尝试的时间，合并状态本身不算合并时刻。失败换渠道时清掉卡和目标上的模型 pin，并清掉等于失败渠道或已排除渠道的目标渠道；这条 `auto_route` 记下的 pin 继续抑制。事件之后写上的渠道或模型保留，即使它曾被排除或低于红线。manual 目标上 design、plan、replan 的整理成功会吸收对应产出并收尾；开发卡仍只回写正文。

## 没做什么，以及原因

未改 `dispose.go`：`Dispose` 与批量路径都走 `decideHarvest`。未改 `design.go`、`api.go`、`internal/route`、`internal/sched`、`internal/merge`、`internal/model`、`internal/e2e`。未改排名、prompt、场景测试。默认退避是 0，因为调度测试要求 transport/timeout 当次即为 ready，那些测试不在本写域。

## 接口请求

调度在截止前不要派发：读卡的 `WaitReason`（`backoff:<RFC3339Nano>`）或 `card:<id>` 上的 `retry_backoff.until`。`internal/merge` 在把卡标为 merged 时追加 `merge_success`（entity `card:<id>`，TS 为成功时间）；目前只改状态，熔断看不到这次合并。`design.go` 的 `recoverUnparsed` 直接调用 `finishNormalizedSource`，不经过 `absorbNormalize`；设计环上的 plan/replan 清单仍不会在那条路径吸收。`route.Request` 仍没有 Exclude。显式指定不过滤红线，只检查健康状态；共享路由接线不在本 lane。`api.go` 的统一入口仍归 R3-C-F1。

## 已知问题

自动模式下相同的 transport、timeout、quota、auth 摘录仍计入重复签名。连续失败那一条不计这些基础设施结果。`Dispose` 的时钟仍是墙上时间，只有 `DevStep` 接受 `WithClock`。

## 修复记录

同渠道 transport/timeout 在配置的退避截止前不可运行，到点后仍走原渠道，次数用尽才换渠道 → internal/loop/dev_apply.go:383, internal/loop/dev.go:207, internal/loop/breaker.go:130 → TestFollowupTransportBackoff
复核排除的 family 来自成功尝试的模型定位行，没有该行才用渠道 family，过期的 AuthorFamily 被覆盖 → internal/loop/dev_apply.go:226, internal/loop/dev_apply.go:253 → TestFollowupReviewFamily
清单中每个剩余的环各断开一条边并各挂一个问题；点名不存在的节点时问题挂到当前节点且正文保留原节点，本轮不失败 → internal/loop/outcome.go:332, internal/loop/dev_apply.go:977 → TestFollowupCardListCycles, TestFollowupUnknownNode
整理结果为 violation 时作废该次尝试并派问题，原卡继续等待复核 → internal/loop/dev_apply.go:94 → TestFollowupNormalizeViolation
auth、breaker、budget、done 投递失败后由持久状态再次投递，已经成功的不再投 → internal/loop/dev_apply.go:554, internal/loop/dev.go:107 → TestFollowupRedeliver
无进展熔断从 merge_success 计时，节点从未记下合并成功时从第一次尝试计时 → internal/loop/breaker.go:404, internal/loop/dev_apply.go:1402 → TestFollowupStallAnchor
显式路由不过滤红线，只检查健康状态 → docs/v2/lane-notes/R3-B.md:16 → TestFollowupFailedRoutePins
失败的自动换渠道清掉卡和目标上残留的模型 pin → internal/loop/dev_apply.go:351 → TestFollowupFailedRoutePins
自动换渠道之后写上的渠道或模型保留，事件自己记下的 pin 继续抑制，低于红线的事后显式指定仍只检查健康 → internal/loop/dev_route.go:82 → TestFollowupFailedRoutePins
manual 目标上整理成功后，design、plan、replan 原卡吸收该种产出并收尾，开发卡仍只回写正文 → internal/loop/dev_apply.go:854 → TestFollowupNormalizeAbsorb
