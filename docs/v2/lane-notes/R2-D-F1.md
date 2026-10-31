# R2-D-F1

## 做了什么
开放问题 type/status 只按大小写和空白归一，认不出就用 decision/open 并保留原文。解析、节点创建/更新/读取、混合旧数据迁移和按状态列表都不拒绝。节点 layer/status 仍严格。

自动路由只在同一 preference、同一 tier、同一 SourceGroup 内比较 Pace，90% 降档也只在该组内。98% 排除、未知余量、红线和显式指定保持原样。

滚动月从原始锚点日推月份，并钳到目标月最后一天。空表版本写入 positioning_meta。List/Get 不返回 `cardex.positioning.version`，Current 不回退 config.json，之后的批准版本仍更大。

提案差异写出 tier 和能力变化。可识别 tier 归一成 T1/T2/T3。认不出的 tier 不使整份批准失败，提案标为待复核，已发布表不收录该原 token。

## 没做什么，以及原因
未调用真实 provider，未做额度探针、派发、调度、route explain 或看板。未改并行 lane 的写域，未改 goals.md 或 design.md。

## 接口请求
无。

## 已知问题
无。

## 修复记录
开放问题不规范枚举软处理并保留原文 → internal/model/open_question.go:101,122；internal/store/open_question.go:59 → TestOpenQuestionIrregularEnumsSoft
Pace 只在同 preference、同 tier、同 SourceGroup 内比较 → internal/route/select.go:111 → TestSelectPaceStaysInsideSource
滚动月从原锚点钳位月末 → internal/quota/window.go:171,198 → TestRollingMonthlyKeepsAnchorDay
提案差异包含 tier 和能力 → internal/positioning/home.go:433 → TestProposalDiffIncludesTierAndCapability
表版本不作为模型行返回，空表版本仍递增 → internal/positioning/home.go:100,265；internal/store/positioning.go:61,95 → TestEmptyPublishHidesVersionRow
可识别 tier 归一，无法识别则待复核且不发布原 token → internal/positioning/home.go:193,366；internal/store/positioning.go:17 → TestApproveNormalizesTierAndMarksUnknown
