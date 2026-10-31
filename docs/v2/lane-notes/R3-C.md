# R3-C

## 做了什么
merge.Service 在临时 worktree 把卡分支合并到集成分支，跑 acceptance 和 test_cmd。通过则快进并删除 worktree。冲突派 rb-，测试变红派 rp-，二者都在原卡分支上续做。新修复的 Deps 指向立即前驱。成功合并后，链上每一张已完成的卡标为 merged 并删除 worktree，再跑一次不变。普通未完成依赖保持原状。
sched.Runner.Tick 走 §6.1 八步。收割结果交给 Runner.Dispose；生产 New 不设置它，也不改终态、不写复核结论。仅 mode=auto 调 designStep/devStep。普通派发和填充共用未完成执行依赖、暂停/取消目标、符号占用（含运行中的 backlog）这三项拒绝。每次 route.Select 都带真实余量和本轮已接受的占用。填充只额外限制临近重置的窗口，一个已触红线的窗口不会被另一个宽裕窗口盖住，也不把渠道写成显式指定。复核检出被复核卡的候选分支（修复链则是原卡分支）后 detach。worktree 或分支已经建好，或卡已 running 但尝试没有 PID 和结束记录时，下一次 Tick 和新 runner 接续同一尝试。notify 的 Origin 是目标的创建会话；已投递但未回答的问题仍交给 notify。
S02、S03、S04、S05、S19、S20 在场景里注入这条调度器。Harness 默认仍是 stub。serve 调用 sched.Serve。

## 没做什么，以及原因
未实现设计 §6.2–§6.6。未改路由算法、看板、通知适配器、prompt 模板、quota 数学、cron-lite 或 internal/model。

## 接口请求
loop 仍只有 DesignStep 和 DevStep。请提供处置函数；调度只在 Dispose 非空时调用。
host 子命令仍打印未实现。调度设置 CARDEX_SCHED_HOST=1，在 init 里调用 host.Run。请把子命令接到 host.Run 后去掉这个环境变量。
prompt.Build 和 notify.Stub.Deliver 仍是 ErrNotImplemented。model.Budget 没有时长，调度把 30m 写到 attempts/<id>/hard-duration。

## 已知问题
rebase 和 repair 若没有 fake 脚本或 scripts/<id>.json，宿主仍会启动，但工作区可能没有解冲突或修好测试，合并会再次失败。

## 修复记录
B1 调度只把收割结果交给处置入口，生产不改终态 → internal/sched/loop.go:178 → TestHarvestDeliversBeforeStatusChange
B2 复核看到未合并的候选分支 → internal/sched/loop.go:967,982 → TestReviewWorkspaceShowsUnmergedCandidate, TestReviewOfRepairShowsRootCandidate
B3 自动派发带真实余量和本轮占用，填充复用路由检查；各窗口余量取最小值，不把快照相加 → internal/sched/loop.go:385,446,640,1042 → TestAutomaticRouteSeesQuotaAndOccupancy, TestFillReusesAutomaticChecks, TestQuotaWindowsKeepTheirOwnRatio
B4 派发和填充拒绝未完成依赖、暂停或取消的目标、已占用符号 → internal/sched/loop.go:885,913 → TestDispatchRefusesUnfinishedDepsPausedClaims
B5 中途失败的尝试由下次 Tick 和新 runner 接续，含已启动的 backlog → internal/sched/loop.go:279,457 → TestPromptFailureResumesSameBranch, TestHostStartFailureResumesOnNewRunner, TestBacklogFailureResumesSameAttempt
B6 通知使用目标 Origin，已投递未回答的问题仍交给 notify → internal/sched/loop.go:831 → TestNotifyUsesObjectiveOriginAndReminds
B7 修复链成功后收束已完成卡和工作区，前驱是立即前驱 → internal/merge/queue.go:155,179,259 → TestQueueRepairChainClosesCompletedCards, TestRepairOfRepairLinksImmediatePredecessor
