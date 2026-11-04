# R2-D-F1-F1-F1

## 做了什么
已批准的空定位表重开后仍算已批准。模型表为空且已有版本时，Current 不回退 config.json 种子，Propose 不以种子为当前表。版本读自己有的 `positioning_meta`（缺表即为无）或空表版本行；下一次 Approve 大于该版本。本包不写 `positioning_meta`，读在发布事务之外。

批准把 `t2`、` T3 ` 收成 T2/T3。未知 token 不使批准失败，提案标待复核，已发布表不收录该 token。

## 没做什么，以及原因
未改 store、model、goals.md、design.md，未恢复版本表写入或迁移，未改 CreatePositioning / UpdatePositioning。未改 CanonicalTier，未做路由、额度、分类、派发或调度。

## 接口请求
转交集成者，沿已有接口请求：恢复独立版本读写及其迁移，版本不作为模型行返回。List/Get 不返回 `cardex.positioning.version` 并迁走该行。本 lane 只读已有 `positioning_meta`，不写它。同时恢复定位 CRUD 软处理及直接 CRUD 回归测试：CreatePositioning、UpdatePositioning（preparePositioning）把 `t2`、` T3 ` 收成 T2/T3，未知 token 丢掉且不硬拒绝。研究字段例外只含 research、开放问题字段和按节点按状态查询，不含这些。

## 已知问题
直接 ListPositioning 仍看得到空表版本行；Current 不返回。

## 修复记录
已批准空表保持版本，空模型表仅在没有已存版本时才算未发布 → internal/positioning/home.go:161,212,323 → TestApprovedEmptyVersionSurvivesReopen
不规范 tier 软处理且不硬拒绝批准 → internal/positioning/home.go:274,446,456 → TestApproveNormalizesTierAndMarksUnknown
