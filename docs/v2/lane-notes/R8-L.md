# R8-L

## 做了什么
集成分支：launchCard 在建 worktree 前调用 Git.EnsureIntegrationBranch。已有引用则返回其 tip；否则按 main、master 读 SHA，用 git branch <名> <该 SHA> 创建。引用已存在即成功。不比较创建后的主分支，不 push，不移动主分支。
launch_failed 经 failLaunch。事件含渠道和模型，没有尝试时也写。到期重试复用上次成功启动以来最近一条带路线的 launch_failed。渠道不在配置或 health 不是 ok 才退回普通选路，并写明丢弃原因。并发满不换渠道。WaitReason 为 backoff: 加 RFC3339Nano。连续三次经 question.Create。尝试终结、dev_disposed 和卡退避在同一个 Store.WithTx。
worktree / prompt / host 三步失败都走 failLaunch。DropLaunch 只删这次新建且 tip 仍等于 base 的 worktree 和卡分支。
Tick 八步各自记 tick_error 后继续。Serve 忽略 Tick 错误。serve-loop.log 打开、写入、关闭失败只写 stderr，循环继续。退出仍只有打不开数据库、已有 config.json 无法解析，和停止信号。迭代日志为 serve loop iteration。

## 没做什么，以及原因
没改 internal/loop、internal/importer、internal/merge 的合并判定、internal/store，也没改 data.go、flags.go、ops.go、surface.go、cli_surface_test.go。BUG-R5A-2 与 BUG-R5A-4 留到切换后。没新增表、列、配置键或停用标志。N2 不在本次范围。

## 接口请求
cmd/cardex/cli_test.go:803 的期望子串从 "serve stub-loop iteration" 改为 "serve loop iteration"。日志按 prompt 第 3 条去掉 stub 后，既有断言会等旧文字直到超时；该文件不在其他 lane 的写域，主对话已放开这一处。

## 已知问题
N2：续作 worktree 已建、卡分支已有提交时，中途失败会留下这次的 worktree。不在本次范围，不单开 lane。
appendPinRelease 失败不清理 worktree，卡可能留在 running。BUG-R5A-2、BUG-R5A-4 仍在。轮内无法解析的 config.json 不令 Serve 退出。
N4 的 launch_failed 事件不在退避事务里，N5 的辅助监听和 serve-addr 失败退出 Serve，都不在本次 findings。

## 修复记录
B1 启动失败后的重试复用失败前的路线，仅渠道缺失或 health 非 ok 才退回 → internal/sched/launch_fail.go:157 internal/sched/launch_route.go:55 internal/sched/loop.go:568 → TestLaunchFailedRouteReused
B2 已按 launch_failed 结束的尝试写 dev_disposed，后续通用处置不再消费 → internal/sched/launch_fail.go:140 → TestAutoNormalizeLaunchFailureRetries
B3 serve-loop.log 打开、写入、关闭失败只写 stderr 并继续 → internal/sched/serve.go:85 → TestServeLoopLogErrorContinues
B4 只改 cli_test.go 一处日志子串 → cmd/cardex/cli_test.go:803 → TestServeLoopsUntilStopped
N1 尝试终结与卡退避同一事务，卡写入失败则回滚 → internal/sched/launch_fail.go:76 → TestLaunchBackoffWriteRollsBack
N3 集成分支按已读 SHA 创建，主分支随后前进不是失败 → internal/workspace/ref.go:72 → TestEnsureIntegrationBranchIgnoresPrimaryAdvance TestDispatchAfterPrimaryAdvances
B1 保存路线的复用在并发已满时等待，且仅指定模型也不跳过该路线 → internal/sched/launch_route.go:38 internal/sched/launch_route.go:75 → TestLaunchFailedRouteReused TestLaunchFailedModelOnlyKeepsRoute
B1-a 渠道并发已满则保留保存路线并等待，不新增尝试、不换渠道、不记丢弃 → internal/sched/launch_route.go:75 → TestLaunchFailedRouteReused TestLaunchFailedModelOnlyKeepsRoute
B1-b 仅指定模型仍复用最近失败路线，渠道删除或 health 非 ok 才回退 → internal/sched/launch_route.go:38 → TestLaunchFailedModelOnlyKeepsRoute
B1-a 有效指定不是显式渠道时保存路线并发已满则等待，不新增尝试、不换渠道、不记丢弃，卡级模型盖过目标渠道也一样 → internal/sched/launch_route.go:41 internal/sched/launch_route.go:56 → TestLaunchFailedModelOnlyKeepsRoute
B1-b 卡仅指定模型、目标指定渠道时，普通派发和 backlog 都复用最近失败路线 → internal/sched/launch_route.go:41 internal/sched/launch_route.go:56 → TestLaunchFailedModelOnlyKeepsRoute
B1-a／B1-b 共同遗漏 是否恢复失败路线按卡级指定优先、否则目标级指定判断，被卡级模型覆盖的目标渠道不阻止恢复 → internal/sched/launch_route.go:41 internal/sched/launch_route.go:56 → TestLaunchFailedModelOnlyKeepsRoute
