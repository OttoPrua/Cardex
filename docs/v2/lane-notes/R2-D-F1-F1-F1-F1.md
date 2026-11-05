# R2-D-F1-F1-F1-F1

## 做了什么
批准把 `t2`、` T3 ` 收成 T2/T3。未知 token 不使批准失败，提案标待复核，已发布表不收录该 token。

## 没做什么，以及原因
未改 internal/store（含 CreatePositioning、UpdatePositioning、preparePositioning）、internal/model、go.mod、goals.md、design.md。store 对不规范 tier 仍返回错误，留给集成者。未改路由、额度、分类、派发、调度，未改卡或渠道的 tier 检查。

## 接口请求
请集成者把已知 tier 收成 T1/T2/T3，丢掉未知 token 且不返回错误，并为定位 CreatePositioning、UpdatePositioning 补直接 CRUD 回归测试。第 0 步的 store 例外仍只含 research、开放问题字段和按节点按状态查询，不含这些。

## 已知问题
直接 ListPositioning 仍看得到空表版本行；Current 不返回。preparePositioning 对 `t2`、` T3 ` 和未知 token 仍返回错误。

## 修复记录
不规范定位 tier 软处理且不硬拒绝批准，未改 internal/store/positioning.go → internal/positioning/home.go:274,446,456 → TestApproveNormalizesTierAndMarksUnknown
