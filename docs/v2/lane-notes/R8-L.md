# R8-L

## 做了什么
集成分支的创建点：派发建卡 worktree 之前，sched.launchCard 调用 workspace.Git.EnsureIntegrationBranch。已有 refs/heads/<名> 则返回其 tip；否则按 refs/heads/main、refs/heads/master 取主分支 HEAD，用 git branch 建引用。重复或并发只留一个引用。不 push，不移动主分支。objective add 与 import-legacy 仍只记录名字。
launch_failed 经 failLaunch。不换渠道。WaitReason 为 backoff: 加 RFC3339Nano，由 releaseDueBackoff 放回 ready。事件记下卡、尝试（有则记）、步骤和至多 2048 字节摘要，不含执行器输出。连续从上次成功启动起算。第三次经 question.Create，kind 为 custom。一次和两次不派问题。
worktree：EnsureIntegrationBranch 失败（含无 main/master）、Git.Create 失败、detachHead 失败。DropLaunch 只删这次新建且 tip 仍等于 base 的 worktree 和卡分支。Ensure 失败时还没有 worktree。不删集成分支。此步通常没有尝试行。
prompt：尝试目录、CreateAttempt、把卡写成 running、writePrompt。尝试 outcome 为 launch_failed，卡离开 running。本次新建的无提交 worktree 和卡分支按上一条删除。resumeAttempt 并未新建它们，两个 drop 都是 false。
host：startAttempt 失败。尝试以 launch_failed 结束，清理范围与本次 scratch 相同。
Tick 出错去向。改造前：reconcile、harvest、advance、merge、ranking、dispatch、schedules、notify 任一步返回错误后，后面的步骤不跑，Serve 随该错误退出。改造后：这八步各自写 tick_error（步骤、卡或目标、摘要）再继续。派发按卡，合并按目标，合并结果不变。同一步骤、同一对象、同一错误 10 分钟只记一次，节流只在内存。写事件失败则摘要进 stderr。launch_failed 在派发内吸收，不写成 tick_error，也不作为 Tick 的致命错误。ctx 取消不记 tick_error，后续步骤不跑。库打不开时只能写 stderr 并返回。Serve 忽略 Tick 的返回值，取消时退出。启动时打不开数据库，或已有 config.json 不是合法 JSON，stderr 写原因并退出。缺少 config.json 不退出。迭代日志为 serve loop iteration。serve listening 只对已有 writer 打一次。model 增加 OutcomeLaunchFailed 与 ErrorLaunchFailed，无表结构变更。

## 没做什么，以及原因
没改 internal/loop（含 dev_apply.go 的 main/master 探测）、internal/importer、internal/merge 的合并判定、internal/store，也没改 data.go、flags.go、ops.go、surface.go、cli_surface_test.go。已有 cmd 测试里只把 cli_test.go:803 的期望从 "serve stub-loop iteration" 改成 "serve loop iteration"，以便 go test ./... 通过。BUG-R5A-2 与 BUG-R5A-4 留到切换后。没新增表、列、配置键或问题种类。appendPinRelease 失败仍向上返回，由 dispatch 记 tick_error，不走 launch_failed。轮内无法解析的 config.json 仍按 schedules() 软成功。

## 接口请求
无。cli_test.go:803 的子串 "serve stub-loop iteration" 已改为 "serve loop iteration"。

## 已知问题
appendPinRelease 失败不清理 worktree，卡可能留在 running。BUG-R5A-2、BUG-R5A-4 仍在。轮内无法解析的 config.json 不令 Serve 退出。
