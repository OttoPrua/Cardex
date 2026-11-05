# R3-A-F1

## 做了什么
未回答的问题按目标、scope、kind、正文复用同一行并重试投递。卡级问题投递到该卡已记录的 origin；没有这条记录，以及节点级问题，仍用目标 origin。`## 可冻结` 的「是」，以及「可以冻结」「是，可以冻结」和写在 `## 结论` 下的明确提议，都会记下可冻结。无法判断的冻结文本先派一张整理卡，整理结果仍无法判断则把源卡标为 needs_input。整理成功后，design、plan、replan 源卡走设计期吸收：开放问题、子节点提议和可冻结提议照设计吸收，plan 与 replan 再落地 `## 新卡`。manual 且仍为 running 的这类源卡可以变成 done。dev 原卡不按设计吸收。

## 没做什么，以及原因
未改 `internal/loop/dev_apply.go`、`internal/sched/`、`cmd/cardex/`、`internal/positioning/`，也未把 ranking 吸收放回设计路径。开发步里非 manual 的整理成功仍只回写文本。`positioning reject` 仍返回 unspecified。

## 接口请求
非 manual 默认整理 `internal/loop/dev_apply.go:820` 只回写文本，不走设计期吸收；`dev_apply.go:683` 直接 `applyDesign` 时，无法判断的可冻结文本仍会把卡标完成。`internal/sched/loop.go:212` 只调用已安装的 Dispose。调度投递和提醒仍用目标 origin（`internal/sched/loop.go:999`）。`internal/positioning/home.go:398` 返回 `positioning reject is unspecified`。`cmd/cardex/origin.go:27` 写入卡 origin，本 lane 只读 `card:<id>` 的 `origin` 事件。

## 已知问题
N2：明确可冻结仍只认小节「可冻结」和「结论」（`internal/loop/design_parse.go:257`）。N10：清单问题的目标节点不是源节点时仍写在源节点（`internal/loop/design.go:1507`）。

## 修复记录
N1 已入库但投递失败的问题复用同一未回答行并重试，不同正文另起一行 → `internal/question/service.go:55` `service.go:82` `internal/loop/design.go:272` `design.go:532` → `TestCreateReusesStoredQuestionAndCardOrigin` `TestQuestionDeliveryRetryOnAbsorb` `TestFreezeQuestionRetriesDelivery`
N2 明确可冻结不依赖固定小节或首词；无法判断走一张整理卡或 needs_input；spike 的非冻结结论不记提议 → `internal/loop/design_parse.go:256` `design_parse.go:292` `internal/loop/design.go:218` `design.go:1288` `design.go:1776` → `TestFreezeWording`
N3 `R3-A.md` 合并重复修复记录后不超过 40 行 → `docs/v2/lane-notes/R3-A.md` → `wc -l`
N6 卡级问题投递该卡 origin 事件，无记录或节点级仍用目标 origin → `internal/question/service.go:126` → `TestCreateReusesStoredQuestionAndCardOrigin`
N9 整理成功后 design、plan、replan 吸收设计产出，plan 与 replan 落地新卡，manual running 源卡可 done；dev 原卡不吸收 → `internal/loop/design.go:1287` `design.go:1324` → `TestNormalizeAbsorbFinishesManualSource`
B1 当前否定压过后文「可以冻结」；裸「否」不整理；没有确定是或否的条件句走一张整理卡且不落地提议 → `internal/loop/design_parse.go:292` `design.go:219` `design.go:1289` `design.go:1831` → `TestFreezeCurrentSense`
B2 投递返回后重读问题行，合并投递记录并保留已保存答复，新建和复用都走这条 → `internal/question/service.go:58` `service.go:66` `service.go:113` → `TestAnswerDuringDeliveryIsKept`
B3 整理吸收的 plan 写入与普通拆卡相同的 plan_split（源 plan、节点、实际创建的卡 ID）；重复吸收不重复建卡；replan 不写 → `internal/loop/design.go:1357` `design.go:1406` `design.go:1486` → `TestNormalizePlanSplitRecordsBatch`
