# R4-D

## 做了什么

S01 在 launchCard 第一次派发前对空 tier 调用 classify.Classify，把等级写回卡并交给 Request.Classified。已有 tier 不调用 Call。生产 Call 为 nil，失败时按 kind 默认（开发卡 T2）。S25 的 facts 只在后续设计或计划卡的事实摘要里同时有问题 id、答案、证据和 Q0。TestFaults 用种子 20261102 一次注入五类故障，终态后再调度，用卡行和事件证明不重复合并。指南 docs/v2/guide.md。skill 只在 skills/cardex-dispatch/SKILL.md。

| 场景 | 状态 |
| --- | --- |
| S01–S22 S24 S25 S26 | 通过 |
| S23 | 通过。状态以 R4-C-F1 为准。集成前若只走辅助函数，不是 R4-D 缺陷 |
| 故障五类 | 通过。非 darwin 的 S24 仍跳过 sandbox-exec is macOS only |

## 没做什么，以及原因

没有改 Tick、dispatch、internal/loop、internal/classify、internal/route、internal/runner、TestS23 及其夹具、design.md、goals.md。没有安装 skill，没有跑真实执行器，没有写 ~/.cardex。交接只有 S23。

## 接口请求

v2 没有单次调用执行器，Channel 没有 classifier 槽。launchCard 只按 id classifier 取渠道名，Runner.Classify 在 New 里保持 nil，分级渠道不会调用模型。请给 Channel 加分级槽并提供一次调用，不要在 runner 里新写。

## 已知问题

无 BUG skip。N1 问题选项、N2 decision 与 otto、X1 skill 定位提案措辞留给后续跟进，本次不改指南和 skill。

## 修复记录

空 tier 在第一次派发前分级，结果写回卡并交给路由；已有 tier 不调用 Call；Call 为空、失败或无法解析时持久化 kind 默认 tier → internal/sched/classify.go:19 internal/sched/loop.go:407 → TestLaunchClassifiesEmptyTier TestS01
后续设计或计划卡的 ## 事实摘要 必须含所答问题的 id、答案和证据，Q0 为 fact 时也含 Q0；只有标题或空列表不算 → internal/e2e/s25_flow_test.go:241 → TestS25
故障套件用一个可复现种子批量注入多卡并覆盖五类；终态后继续调度，用库中的卡状态和事件证明不重复合并；空计数不是证据；无问题的 needs_input 不能跳过 → internal/e2e/faults_test.go:24 → TestFaults
