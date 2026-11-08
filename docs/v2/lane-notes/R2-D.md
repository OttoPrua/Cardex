# R2-D

## 做了什么
`positioning.Home` 把已批准定位表放在 home 的 `cardex.db`，用一次存储事务发布单调版本。两个 `Open` 不能把同一版本各写成下一版。空表不把版本退回 1。还没有批准时，Current 仍是该 home 的 `config.json` 里 `positioning` 段。Propose 只写提案文件。

`quota.Ledger` 支持 5h、daily、weekly、monthly，重置可以是固定时刻或滚动。Record 把已知 token 计入覆盖的窗口；token 为 0 且来源为空时按时长估算，置信度更低。一次 Record 也计一次请求。剩余比例是 `1-已用/上限`；token 与请求上限都有时取更紧者；都没有则为未知。ObserveLimit 在观测到的重置时间前硬封锁，只校正唯一对上的窗口。

`route.Select` 按设计 §7.1 的六步选择。显式渠道或模型只检查健康，不借级。复核卡去掉与作者同 family 的候选。本层过滤后没有可用候选时只向上借一级，并保留两层过滤理由。过滤健康、并发、红线、封锁、超过 98% 和 `unknown_quota: exclude`。同 preference、同层、同源按 Pace 排序；已用超过 90% 降一档。`unknown_quota` 默认 allow。Claude 5 小时保底是渠道 `redline`，示例在 `internal/quota/testdata/claude-5h.config.json`。

`classify.Classify` 在卡上已有 tier 时不调用。否则用包内固定 prompt 对 classifier 渠道做一次调用，期限 30 秒，按正文哈希缓存。超时、解析失败或渠道不可用时退回 kind 的默认 tier。

供 R3 使用的开放问题：`model.OpenQuestion` 字段为 id、text、type、status、asks、answer、evidence、confidence、commit、cited_paths。类型 fact、experiment、decision、owner；状态 open、researching、answered、stale、unanswered。查询方法是 `(*store.Store).ListOpenQuestions`。旧字符串读成 text，type=decision，status=open。`research` 的默认 tier 是 T1。

## 没做什么，以及原因
未调用真实模型，未做额度探针、派发、`sched.Loop`、backlog 填充、`cardex route explain`、看板和定位 CLI。Reject 不修改 Current。

## 接口请求
store 没有与定位行无关的表版本。空表版本暂存在 Current 不返回的内部行。最小改法：增加单例版本，List/Get 不把它当模型，然后删除该内部行。

## 已知问题
直接 `ListPositioning` 会看到内部版本行；Current 不会。

## 修复记录
批准版本在共享库事务里单调增加，空表不归零 → internal/positioning/home.go:172,192,212 → TestPublishVersionIsMonotonicAcrossOpens
本层过滤后没有可用候选才向上借一级 → internal/route/select.go:40,84 → TestBorrowOnlyAfterTierIsUnusable
一次撞线只校正对得上的窗口 → internal/quota/ledger.go:190 → TestObserveLimitKeepsOtherWindows
请求上限参与剩余比例，一次记录计一次 → internal/quota/ledger.go:52,83,250；internal/route/select.go:305,322,350 → TestRequestCapRemainingRatio
发布没提交成功时不得当成批准，nil 等待不能盖掉事务错误 → internal/positioning/home.go:185,188,202,209 → TestApprovePersistentLockConflict
能确定唯一窗口时用观测重置校正，不必先等于旧预测 → internal/quota/ledger.go:219,221 → TestObserveLimitCorrectsDeterminedWindow, TestSelectRecoversAfterMisalignedReset
