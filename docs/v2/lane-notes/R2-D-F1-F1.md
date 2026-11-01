# R2-D-F1-F1

## 做了什么
Current 和 Propose 共用 currentLocked。定位行和表版本在同一次 WithTx 里读取；两读不一致时整份保留先读到的快照，不用新版本给旧行盖章，也不把发布前的空读和发布后的版本拼成空表。

批准仍用原来的发布事务。版本来自定位行；空表把版本写入 `cardex.positioning.version`，Current 不返回该行。可识别 tier 在定位层归一成 T1/T2/T3；认不出则提案待复核，不发布原 token。

## 没做什么，以及原因
未把定位 CRUD 的 tier 改写、独立版本表及其迁移留在 model/store。CanonicalTier 在 model。第 0 步的 research、开放问题字段、按节点按状态查询及其迁移仍保留。未改路由、额度、分类、派发、调度，未改 goals.md 或 design.md。

## 接口请求
请集成者补回已拆出的共享定位修复：定位 CRUD 归一 tier、独立版本表（不作为模型行返回）及其迁移。研究字段例外只含 research、开放问题字段和按节点按状态查询，不含这些。最小改法：版本单独存放，List/Get 不返回 `cardex.positioning.version` 并迁走该行；Create/Update 把可识别 tier 收成 T1/T2/T3，丢掉无法识别的 token，不要硬拒绝。

## 已知问题
空表版本仍是一条定位行。直接 ListPositioning 看得到；Current 不返回。

## 修复记录
定位内容和版本必须来自同一快照 → internal/positioning/home.go:104,266 → TestCurrentAndProposeReadOneSnapshot
定位 CRUD 的 tier 改写和独立版本表不进 store，tier 归一留在定位层 → internal/store/positioning.go:22；internal/store/store.go:193；internal/positioning/home.go:228,392 → TestApproveNormalizesTierAndMarksUnknown
拆出共享实现时，不能删除同轮产出仍依赖的已集成接口 → internal/model/enums.go:307 → TestCanonicalTier
