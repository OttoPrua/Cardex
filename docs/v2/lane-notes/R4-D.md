# R4-D

## 做了什么

S01 取消 skip：空 tier 单卡走生产 Dispose，另一 family 复核，轮询见到 merge_queued 先于 merged。S25 对一个 mode=auto 目标按首次成立记录 inventory、facts、frozen、module、split、skeleton、parallel、close。TestFaults 覆盖 transport、truncated、quota、auth、serve-restart；serve-restart 杀掉 host 后再调 sched.Serve。指南在 docs/v2/guide.md。skill 只在仓库 skills/cardex-dispatch/SKILL.md。

| 场景 | 状态 |
| --- | --- |
| S01 S02 S03 S04 S05 S06 S07 S08 S09 S10 S11 S12 S13 | 通过 |
| S14 S15 S16 S17 S18 S19 S20 S21 S22 S25 S26 | 通过 |
| S23 | 通过。排名仍由测试调用 loop.AbsorbHarvestedRanking 和 loop.FollowRanking，不是只走生产处置。这是 R4-C-F1 的结果，不是 R4-D 缺陷 |
| S24 | 通过。非 darwin 仍跳过 sandbox-exec is macOS only |
| transport truncated quota auth serve-restart | 通过 |

## 没做什么，以及原因

没有改 internal/e2e 以外的 Go，没有改 TestS23 及其专用夹具，也没有改写已通过的 S02–S22、S24、S26。没有安装 skill，没有跑真实执行器，没有写 ~/.cardex，没有 launchd，没有绑定 8787/8788。交接没有写域外事项，所以没有另列未实现项。

## 接口请求

sched.launchCard（internal/sched/loop.go）选路时不填 Request.Classified，也不调用 classify.Classify。route.resolveTier（internal/route/select.go）在卡 tier 为空时用 kind 默认等级，且不写回卡。请在派发前分级并把等级存到卡上。S01 只在 attempt.RouteReason 的候选等级上看到 dev 的默认 T2，卡.Tier 仍为空。

## 已知问题

两次 internal/e2e 与两次全量测试均无失败，无 BUG skip。S23 的 helper 路径见上，不记为本 lane 缺陷。
