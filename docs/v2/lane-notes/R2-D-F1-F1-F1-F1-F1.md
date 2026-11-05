# R2-D-F1-F1-F1-F1-F1

## 做了什么
定位写入对不规范 tier 软处理。CreatePositioning 与 UpdatePositioning 共用的 preparePositioning 把 `t2`、` T3 ` 收成 T2/T3，丢掉未知 token（含空白），整次写入仍成功。已是 T1/T2/T3 的原样写回。nil 定位和空 id 的更新仍失败。批准路径此前已收 tier，本轮未改。

## 没做什么，以及原因
未改 internal/route 的逐字 tier 比较，未改卡或渠道的 tier 检查，未改 internal/model、quota、classify、goals.md、design.md。未给 store 增加待复核状态。

## 接口请求
无。

## 已知问题
直接 route.Select 仍逐字比较定位 tier。T2 卡、唯一健康且有空闲并发的渠道及其模型、定位 Tiers=["t2"]、没有其他候选时返回 ErrNoCandidate；" T3 " 对 T3 卡同样如此。

## 修复记录
定位写入对不规范 tier 软处理：已知 token 收成 T1/T2/T3，未知 token 丢掉且不拒绝整次写入 → internal/store/positioning.go:22 → TestPositioningWriteSoftensNonCanonicalTiers
说明只覆盖已验证的批准入口和定位写入，不把直接 route.Select 说成不会因不规范定位 tier 返回错误 → docs/v2/lane-notes/R2-D-F1-F1-F1-F1.md:4; docs/v2/lane-notes/R2-D-F1-F1-F1-F1-F1.md:13 → 无
