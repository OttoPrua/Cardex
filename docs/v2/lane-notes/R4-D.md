# R4-D

## 做了什么

S01 在 launchCard 第一次派发前对空 tier 调用 classify.Classify，把等级写回卡并交给 Request.Classified。已有 tier 不调用 Call。生产 Call 为 nil，失败时按 kind 默认（开发卡 T2）。S25 的事实摘要含问题 id、答案、证据和 fact Q0。TestFaults 用种子 20261102 一次注入五类故障，终态后再调度，用卡行和事件证明不重复合并。指南 docs/v2/guide.md。skill 只在 skills/cardex-dispatch/SKILL.md。

事故：2026-11-04 02:01、02:21、04:22 三次 `syscall.Kill(-1, SIGKILL)` 杀掉 launchd 任务。seedQuotaBlock 写 PID 1，stopLaunchHosts 遍历尝试，killHost 向 -pid 发 SIGKILL。A（c7d92a9）只经 internal/host/signal.go 发信号，拒绝 pid 或 pgid ≤ 1、本进程、本进程组。A2：测试且 `CARDEX_OUTER_SANDBOX=1` 时 Wrap 走 Skip，记一条 sandbox_skipped，不调用 sandbox-exec。生产二进制忽略 `CARDEX_OUTER_SANDBOX`。只有断言真实沙箱拒绝的测试才跳过。扫描匹配 syscall.Kill(、unix.Kill(、.Process.Kill( 和带参数的 .Signal(，ws.Signal() 不算，无白名单。本口径是 R4-D.rework.md（2026-11-04）。

| 场景 | 状态 |
| --- | --- |
| S01–S22 S24 S25 S26 | 本轮 `CARDEX_OUTER_SANDBOX=1` 下 go test 通过。S24 因断言真实沙箱拒绝而跳过，lane.sh test 第二步再跑 |
| S23 | 未改。状态以 R4-C-F1 为准。集成前若只走辅助函数，不是 R4-D 缺陷 |
| 故障五类 | TestFaults 通过，断言未放宽 |

B2 仍成立（R4-D.rework-1.md）：后续设计或计划卡的 ## 事实摘要 含所答问题 id、答案和证据，Q0 为 fact 时含 Q0；只查标题不算。
B3 仍成立（R4-D.rework-1.md）：种子 20261102 一批注入 transport、截断流、quota、auth、以及 host kill 后 serve 重启；终态后继续调度，用卡行和事件证明不重复合并。

## 没做什么，以及原因

没有改 Tick、dispatch、fill、internal/loop、internal/classify、internal/route、internal/runner、internal/model、TestS23 及其夹具、design.md、goals.md。没有安装 skill，没有跑真实执行器，没有写 ~/.cardex。交接只有 S23：R4-C-F1 写该测试函数和专用夹具，本 lane 不改。写域外剩余：把已 merged 的卡改回 merge_queued 再 Tick，会再次进入 internal/merge 的 mergeOne 并重跑验收，且不追加事件。本 lane 不改 internal/merge，TestFaults 不强制这条回路。

## 接口请求

v2 没有单次调用执行器，Channel 没有 classifier 槽。launchCard 只按 id classifier 取渠道名，Runner.Classify 在 New 里保持 nil，分级渠道不会调用模型。请给 Channel 加分级槽并提供一次调用，不要在 runner 里新写。
上一轮源码扫描命中和 sandbox-exec 退出 71 已由 A2 在写域内修掉。S01、S25、TestFaults 通过，断言未放宽，没有新的写域外失败。
强制把已 merged 的卡改成 merge_queued 再 Tick，会再次进入 internal/merge 的 mergeOne/runChecks。验收命令再执行一次，状态回到 merged，事件 id 不变。请合并路径在没有新提交时不要重跑验收。本写域不改 internal/merge，TestFaults 只断言额外 tick 不增加合并验收次数。

## 已知问题

无 BUG skip。N1 问题选项、N2 decision 与 owner、X1 skill 定位提案措辞已写入指南和 skill，见下方修复记录。

## 修复记录

空 tier 在第一次派发前分级，结果写回卡并交给路由；已有 tier 不调用 Call；Call 为空、失败或无法解析时持久化 kind 默认 tier → internal/sched/classify.go:19 internal/sched/loop.go:407 → TestLaunchClassifiesEmptyTier TestS01
后续设计或计划卡的 ## 事实摘要 必须含所答问题的 id、答案和证据，Q0 为 fact 时也含 Q0；只有标题或空列表不算 → internal/e2e/s25_flow_test.go:241 → TestS25
故障套件用一个可复现种子批量注入多卡并覆盖五类；终态后继续调度，用库中的卡状态和事件证明不重复合并；空计数不是证据；无问题的 needs_input 不能跳过 → internal/e2e/faults_test.go:24 → TestFaults
2026-11-04 02:01、02:21、04:22：`go test ./internal/sched/ -run 'TestLaunchUnusableChannel...'` 三次 `syscall.Kill(-1, SIGKILL)`，杀掉 278、262、191 个 launchd 任务。seedQuotaBlock 写入 PID 1，stopLaunchHosts 遍历全部尝试，killHost 在 pid>0 时向 -pid 发 SIGKILL。
pid 或 pgid ≤ 1、本进程、本进程组拒绝且不调用系统调用；系统调用经 signalKill 注入 → internal/host/signal.go:18 internal/host/signal.go:27 internal/host/signal.go:34 → TestSignalGuard
killGroup、processGroupAlive、processAlive、runAcceptance 取消只走该入口 → internal/host/deadline.go:177 internal/host/deadline.go:73 internal/host/reconcile.go:57 internal/harvest/harvest.go:142 → TestSignalGuard
清理只向本次 Launch 记下的 pid 发信号，不遍历尝试表；seedQuotaBlock 的 PID 为 0；占坑用测试启动的 sleep → internal/sched/loop_test.go:726 internal/sched/loop_rework_test.go:485 internal/sched/launch_pin_test.go:407 internal/e2e/scenarios_test.go:2517 internal/e2e/faults_test.go:520 → TestLaunchUnusableChannel
指定渠道缺失、健康不是 ok、或在已知重置前被封锁，才放开本次请求副本；余量低于红线不放开；写回卡的渠道和模型，事件 pin_released；目标 Channel 与 Model 不改；无候选则 needs_input 且同一原因只问一次，不 failed，不增加尝试；backlog 与健康未封锁的指定保持原路 → internal/sched/launch_route.go:141 internal/sched/loop.go:414 → TestLaunchUnusableChannel
B2 仍成立：后续设计或计划卡的 ## 事实摘要 含所答问题 id、答案和证据，Q0 为 fact 时含 Q0；只查标题不算 → internal/e2e/s25_flow_test.go:308 internal/e2e/s25_flow_test.go:325 → TestS25
B3 仍成立：种子 20261102 一批注入 transport、截断流、quota、auth、以及 host kill 后 serve 重启；终态后继续调度，用卡行和事件证明不重复合并 → internal/e2e/faults_test.go:19 → TestFaults
S23 未改。状态以 R4-C-F1 为准。集成前若只走辅助函数，不是 R4-D 缺陷。
测试进程且 CARDEX_OUTER_SANDBOX=1 时 Wrap 走与非 macOS 相同的 Skip，记一条 sandbox_skipped，不调用 sandbox-exec；生产二进制忽略该变量 → internal/sandbox/api.go:24 internal/sandbox/api.go:35 → TestWrapOuterSandbox
只在同时满足 testing.Testing 与 CARDEX_OUTER_SANDBOX=1 时跳过断言真实沙箱拒绝的测试，其余测试照跑 → internal/sandbox/api.go:44 internal/sandbox/sandbox_test.go:60 internal/sandbox/sandbox_test.go:109 internal/host/host_test.go:545 internal/e2e/scenarios_test.go:1845 → TestWrapDeniesOutsideWrite TestWrapAllowsLocalNetwork TestRunDeniesAttemptDirWrite TestS24
源码扫描零命中且无白名单：syscall.Kill(、unix.Kill(、.Process.Kill( 以及带参数的 .Signal( 才算命中；ws.Signal() 不算。测试里的探测和停进程改走信号入口 → internal/host/host_test.go:171 internal/host/host_test.go:430 cmd/cardex/cli_surface_test.go:1218 internal/host/signal_guard_test.go:140 → TestSignalGuard
合并验收按夹具安装、真实合并路径执行的命令计数；工作目录含 cardex-merge- 才算合并，收割另计。第一次成功后次数为 1，额外 tick 不增加。空计数不是证据。参与卡必须是 merged、非代码卡的 done，或带明确问题。waiting、paused、failed 且没有问题，以及 ready、running、harvesting、reviewing、merge_queued 都失败 → internal/e2e/faults_test.go:410 internal/e2e/faults_test.go:472 internal/e2e/faults_test.go:512 → TestFaults
有效钉不可用时，本次请求的卡副本和目标副本都去掉该指定，再走自动选择并仍过滤红线；库存目标的 Channel 与 Model 不改。卡钉缺失渠道 A、目标钉健康但低于红线的渠道 B、存在健康候选 C 时启动用 C；没有这样的 C 则 needs_input，不发给 B。余量低于红线本身不放开仍有效的显式钉 → internal/sched/launch_route.go:32 internal/sched/launch_route.go:106 internal/sched/launch_pin_test.go:115 → TestLaunchUnusableChannel
模型钉只在每个承载渠道都缺失、健康不是 ok、或封锁到已知重置时才放开。仍有健康未封锁的承载则保留模型，并从请求副本去掉不可用承载。更高优先级、只提供别的模型的渠道不会抢走这次启动。目标级模型钉同样。两条规则同时在 releaseUnusablePin 和 chooseLaunchRoute 的回退里生效 → internal/sched/launch_route.go:51 internal/sched/launch_route.go:143 internal/sched/launch_route.go:189 internal/sched/launch_route.go:243 internal/sched/launch_pin_test.go:131 → TestLaunchUnusableChannel
生产调度不再导出 LaunchHost。端到端测试在自己的辅助函数里启动 host，只记录本次启动且大于 1 的 pid，信号只走 host.SignalGroup 和 host.SignalProcess → internal/sched/launch.go:47 internal/e2e/scenarios_test.go:2484 internal/e2e/scenarios_test.go:2511 internal/e2e/scenarios_test.go:2522 → TestSignalGuard
回答问题要先用 `cardex --json question show <id>` 读 text、options 和 answer。普通 question show 只有 id 和答案。question list 包含已经回答的问题 → docs/v2/guide.md:53 skills/cardex-dispatch/SKILL.md:23 skills/cardex-dispatch/SKILL.md:27 → 无新测试夹具
`[decision]` 由设计卡权衡，问题留在设计节点上，不生成 使用者（owner） 待办。`[owner]` 需要 使用者（owner） 决定 → docs/v2/guide.md:45 → 无新测试夹具
管理会话不再被要求只能批准已经选定的定位提案。方向冻结、预算耗尽、熔断和目标完成仍只交给 使用者（owner），不扩大其他授权 → skills/cardex-dispatch/SKILL.md:31 skills/cardex-dispatch/SKILL.md:35 → 无新测试夹具
