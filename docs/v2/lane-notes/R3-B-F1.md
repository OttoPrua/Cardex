# R3-B-F1

## 做了什么

同一条 decideHarvest 处置开发收割。未配置时同渠道 transport/timeout 第一次等 30 秒，同一张卡再等为基数的两倍；显式 retry_backoff_seconds（含 0）替换基数，0 仍当次 ready。截止前为 waiting；DevStep 与 dispatch 开头释放到期的 backoff:。次数用尽后换渠道且不等待。换渠道只改本卡渠道并清本卡模型 pin，不改存储的目标 Channel/Model；抑制只作用本卡这次请求，同目标另一张卡仍用目标显式渠道和模型。复核 family 取成功尝试的模型定位行。整理为 violation 时作废尝试并派问题。清单每个剩余环各断一条边并各挂问题；点名不存在的节点挂到当前节点。投递失败后按 notice_due 再投。新标 merged 时追加带时间的 merge_success。无进展：任一已合并卡缺少自己的 merge_success 时整节点未知，不用另一张卡的旧事件暂停 ready 卡或挂熔断；每张都有时间用最晚的；从未合并用第一次尝试；单张已合并无事件不熔断。manual 的 design、plan、replan 在 running 时整理成功也吸收并收尾；开发卡只回写。

## 没做什么，以及原因

未改 design.go、api.go、goals.md、design.md、internal/route。未改 Tick、Runner、New、launchCard、advance。合并的冲突、修复、变基、快进未改，只在新变为 merged 时记事件。未加 Exclude，未对显式 pin 做红线过滤。

## 接口请求

design.go recoverUnparsed（1362）直接 finishNormalizedSource，不经 absorbNormalize，plan/replan 清单仍不吸收。sched advance（loop.go:221）仍跳过非 auto。route.Request 仍无 Exclude。api.go 统一入口归 R3-C-F1。

## 已知问题

自动模式下相同 transport、timeout、quota、auth 摘录仍计入重复签名；连续失败路径不计。Dispose 时钟是墙上时间，只有 DevStep 接受 WithClock。

## 修复记录

未配置同渠道第一次退避 30 秒、再次为两倍；显式值含 0 替换基数；dispatch 释放到期 backoff:，第三次失败换渠道且不等待 → internal/loop/breaker.go:42, internal/loop/dev_apply.go:378, internal/sched/backoff.go:14, internal/sched/loop.go:258 → TestFollowupTransportBackoff, TestDispatchBackoffRelease, TestDispatchThirdTransportSwitches
复核 family 取成功尝试的模型定位行，过期 AuthorFamily 被覆盖 → internal/loop/dev_apply.go:226 → TestFollowupReviewFamily
清单每个剩余环各断一条边并各挂问题；点名不存在的节点挂当前节点 → internal/loop/dev_apply.go:731, internal/loop/outcome.go:379 → TestFollowupCardListCycles, TestFollowupUnknownNode
整理为 violation 时作废尝试并派问题 → internal/loop/dev_apply.go:93 → TestFollowupNormalizeViolation
投递失败后按 notice_due 再投，已成功的不再投 → internal/loop/dev_apply.go:580 → TestFollowupRedeliver
新标 merged 追加带时间的 merge_success → internal/merge/queue.go:162 → TestMergeSuccessRecordedOnce
同节点任一已合并卡没有自己的 merge_success 时，不用另一张卡的旧事件认定长时间未合并，不暂停 ready 卡、不挂 no merge within stall window；每张都有时间用最晚的，从未合并用第一次尝试，单张已合并无事件保持未知 → internal/loop/dev_apply.go:1435, internal/loop/breaker.go:411 → TestFollowupStallAnchor
显式路由不过滤红线，只检查健康状态 → docs/v2/lane-notes/R3-B.md:16 → TestFollowupFailedRoutePins
单张卡自动换渠道不清除目标 Channel 和 Model；抑制只作用本卡本次请求，同目标另一张卡仍选该显式渠道和模型 → internal/loop/dev_apply.go:348, internal/loop/dev_route.go:86 → TestFollowupFailedRoutePins
换渠道之后写上的渠道或模型保留 → internal/loop/dev_route.go:82 → TestFollowupFailedRoutePins
manual 的 design、plan、replan 原卡在 running 时整理成功也吸收并收尾，开发卡只回写 → internal/loop/dev_apply.go:863 → TestFollowupNormalizeAbsorb
