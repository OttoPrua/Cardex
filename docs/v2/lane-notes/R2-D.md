# R2-D

## 做了什么
`positioning.Home` 把定位表放在调用方 home 下。Current 返回已批准版本；还没有批准时，用该 home 的 `config.json` 里 `positioning` 段。Propose 只写提案。Approve 把它写成新的当前版本，版本号大于上一版。

`quota.Ledger` 支持 5h、daily、weekly、monthly，重置可以是固定时刻或滚动。Record 把已知 token 计入覆盖的窗口；token 为 0 且来源为空时按时长估算，置信度低于已知用量。Remaining 在有上限的窗口里取最小剩余比例 `1-used/limit`，没有上限则为未知。ObserveLimit 在观测到的重置时间前硬封锁，并把窗口锚点改到该时刻。

`route.Select` 按设计 §7.1 的六步选择。显式渠道或模型只检查健康。复核卡去掉与作者同 family 的候选。本层没有候选时只向上借一级。过滤健康、并发、红线和封锁。同 preference、同层、同源按 Pace 排序；已用超过 90% 降一档，超过 98% 排除。`unknown_quota` 默认 allow。理由含候选、每条过滤原因和分数。Claude 5 小时保底是渠道 `redline`，示例在 `internal/quota/testdata/claude-5h.config.json`。

`classify.Classify` 在卡上已有 tier 时不调用。否则用包内固定 prompt 对 classifier 渠道做一次调用，期限 30 秒，按正文哈希缓存。超时、解析失败或渠道不可用时退回 kind 的默认 tier。

供 R3 使用的开放问题：`model.OpenQuestion` 字段为 id、text、type、status、asks、answer、evidence、confidence、commit、cited_paths。`model.OpenQuestionType` 取 fact、experiment、decision、otto。`model.OpenQuestionStatus` 取 open、researching、answered、stale、unanswered。按节点和状态查询的方法是 `(*store.Store).ListOpenQuestions`。旧的字符串数组读成 text，type=decision，status=open。`research` 的默认 tier 是 T1，由 `model.DefaultTier` 给出。

## 没做什么，以及原因
未调用真实模型，未做额度探针、派发、`sched.Loop`、backlog 填充、`cardex route explain`、看板和定位 CLI。这些属于别的 lane，或设计写明 v2.0 不做。提案拒绝没有产品定义，Reject 不修改 Current。

## 接口请求
无。

## 已知问题
无。
