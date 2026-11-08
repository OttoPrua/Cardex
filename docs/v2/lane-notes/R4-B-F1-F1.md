# R4-B-F1-F1

## 做了什么

生成标识与保留原文分开。安全原文原样保留，其余值用 `g` 加十六进制，无 id 的卡用 `v1g-`。priority、soft-tokens、hard-tokens、redline、concurrency 不是数字时保留旧值或默认值，报告 needs correction，其余合法字段照常写入。queued、running、held（含 running workflow）的 step 大于 prompt 数时保留原 step，正文标待复核，prompt 留在待执行。

## 没做什么，以及原因

未改 question、sched、positioning、model、e2e、design.md、goals.md。未实现 Reject，未给 Card 加 Origin。未合并同一显式 id 的两条记录。未知旗标、未知动词、缺 id、建渠道缺 runner 和帮助仍硬退出。

## 接口请求

N6：卡级问题已用卡 origin 事件，否则用目标 origin（internal/question/service.go:129，internal/sched/deliver.go:33）。`positioning reject <id>` 仍返回 positioning reject is unspecified（internal/positioning/home.go:398）。CLI 只交出该错误。请在 positioning 写域实现 Reject。

## 已知问题

doctor 仍不把未知额度重置规则当问题。同一显式 id 的两条 v1 记录仍由 seen 丢掉后一条。

## 修复记录

N1 生成标识不得与保留原文落到同一 id，无 id 记录不得因生成值等于显式 id 被丢掉 → internal/importer/place.go:281 internal/importer/place.go:299 internal/importer/import.go:53 → TestImportGeneratedIDsStayDistinct
N2 已识别业务数值无效时保留旧值或默认值并报告待修正，其余合法字段照常应用 → cmd/cardex/flags.go:96 cmd/cardex/ops.go:92 cmd/cardex/data.go:168 cmd/cardex/surface.go:532 → TestSoftBusinessValues
N6 卡 origin 投递已在 question 与 sched，提案拒绝仍是 unspecified，本 lane 不改 → internal/question/service.go:129 internal/sched/deliver.go:33 internal/positioning/home.go:398 → TestCreateReusesStoredQuestionAndCardOrigin
N8 step 大于 prompt 数是未知进度，保留 step 与待执行并标待复核 → internal/importer/place.go:182 → TestImportContinuableBodyKeepsStepBoundaries
