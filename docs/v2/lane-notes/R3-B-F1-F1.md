# R3-B-F1-F1

## 做了什么

空定位 family 回退成功渠道 family，复核候选按该 family 排除。自动换渠道后，卡或目标上后写且与记录 pin 不同的 Model，若被排除渠道能提供它，则保留该渠道；与记录相同的模型不恢复。认证通知 id 仍是 auth-<channel>，episode 为尝试 id；同 episode 去重，新 episode 投递失败可再投。待投递 origin 为空时用通知所属目标的 origin，仍空则用 config 的 notify.fallback。design、plan、replan 的整理成功走 finishNormalizedSource；直接 plan/replan 的模糊冻结走 recoverUnparsed。

## 没做什么，以及原因

未改 design.go、dispose.go、api.go、sched、model、go.mod、design.md、goals.md。N4 的默认 Tick 不在写域。未实现 R9-E，未改其他 lane 的路径。

## 接口请求

internal/sched 的通知阶段应排空 notice_due，而不是只扫描 Question。包括 manual 目标：认证通知第一次投递失败、尝试已 dev_disposed 之后，默认 Tick 成功时要再次投出。本 lane 只保证该目标之后的 DevStep 会重投。

## 已知问题

budget-reminder-<obj> 仍是空 episode，花费守卫使它只发一次。design.go 的 designFamily 仍先读卡上已写入的 AuthorFamily，不在本写域。

## 修复记录

N1 空定位 family 回退成功渠道，候选排除该 family 而不只排除旧 family → internal/loop/dev_apply.go:252 → TestFollowupR6ReviewSelect
N2 换渠道后新的卡级或目标级 Model 保留其唯一渠道，未新写的模型仍排除 → internal/loop/dev_route.go:106 → TestFollowupR6LaterModelPin
N3 同渠道后一次认证失败按新 episode 重投，同一次不重复 → internal/loop/dev_apply.go:627 → TestFollowupR6AuthEpisode
N4 manual 认证通知在尝试已处置且投递失败后，同目标 DevStep 再投出；Tick 消费者见接口请求 → internal/loop/dev_apply.go:568 → TestFollowupR6AuthEpisode
N5 重投 origin 取通知所属目标，空 origin 用 notify.fallback，不用当前步骤目标 → internal/loop/dev_apply.go:665 → TestFollowupR6NoticeOrigin
N6 plan/replan 整理吸收开放问题与清单，缺清单不因此 needs_input → internal/loop/dev_apply.go:952 → TestFollowupR6NormalizeAbsorb
N9 直接 plan/replan 的模糊可冻结或结论派一张整理卡，且不把源卡标完成 → internal/loop/dev_apply.go:738 → TestFollowupR6UnclearFreeze
B1 设计类整理成功后该次尝试记下设计推进认的 loop.absorbed，避免同一产出再应用 → internal/loop/dev_apply.go:966 → TestFollowupR6NormalizeOnce
