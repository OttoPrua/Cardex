# R4-D

## 做了什么

S25 对一个 mode=auto 目标按首次成立记录 inventory、facts、frozen、module、split、skeleton、parallel、close。TestFaults 覆盖 transport、truncated、quota、auth、serve-restart；serve-restart 杀掉 host 后再调 sched.Serve。指南在 docs/v2/guide.md。skill 只在仓库 skills/cardex-dispatch/SKILL.md。

| 场景 | 状态 |
| --- | --- |
| S02 S03 S04 S05 S06 S07 S08 S09 S10 S11 S12 S13 | 通过 |
| S14 S15 S16 S17 S18 S19 S20 S21 S22 S25 S26 | 通过 |
| S01 | BUG-1。调度不分级，见已知问题 |
| S23 | 通过。排名仍由测试调用 loop.AbsorbHarvestedRanking 和 loop.FollowRanking，不是只走生产处置。这是 R4-C-F1 的结果，不是 R4-D 缺陷 |
| S24 | 通过。非 darwin 仍跳过 sandbox-exec is macOS only |
| transport truncated quota auth serve-restart | 通过 |

## 没做什么，以及原因

没有改 internal/e2e 以外的 Go，没有改 TestS23 及其专用夹具，也没有改写已通过的 S02–S22、S24、S26。没有安装 skill，没有跑真实执行器，没有写 ~/.cardex，没有 launchd，没有绑定 8787/8788。交接没有写域外事项，所以没有另列未实现项。

## 接口请求

sched.launchCard（internal/sched/loop.go）选路时不填 Request.Classified，也不调用 classify.Classify。route.resolveTier（internal/route/select.go）在卡 tier 为空时用 kind 默认等级，且不写回卡。请在派发前分级并把等级存到卡上。

## 已知问题

BUG-1：空 tier 的 dev 卡经 sched.New 入队并 Tick 后，attempt.RouteReason 的候选等级只是 kind 默认 T2，卡.Tier 仍为空，没有 classify 的 Source 或 Reason。复现：KindDev、Tier 空、Status ready 的卡交给生产 Runner.Tick。定位：internal/sched/loop.go 的 launchCard 构造 route.Request 时不填 Classified；internal/route/select.go 的 resolveTier 落到 model.DefaultTier。S23 的 helper 路径见上，不记为本 lane 缺陷。
