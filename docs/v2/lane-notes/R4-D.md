# R4-D

## 做了什么

S01 在 launchCard 第一次派发前对空 tier 调用 classify.Classify，把等级写回卡并交给 Request.Classified。已有 tier 不调用 Call。生产 Call 为 nil，失败时按 kind 默认（开发卡 T2）。S25 的 facts 只在后续设计或计划卡的事实摘要里同时有问题 id、答案、证据和 Q0。TestFaults 用种子 20261102 一次注入五类故障，终态后再调度，用卡行和事件证明不重复合并。指南 docs/v2/guide.md。skill 只在 skills/cardex-dispatch/SKILL.md。

| 场景 | 状态 |
| --- | --- |
| S01–S22 S24 S25 S26 | 本轮 go test 未通过：sandbox-exec 退出 71 |
| S23 | 未改。状态以 R4-C-F1 为准。集成前若只走辅助函数，不是 R4-D 缺陷。本轮排名执行器同样是 sandbox-exec 退出 71 |
| 故障五类 | 本轮 go test 未通过：sandbox-exec 退出 71。断言未放宽 |

## 没做什么，以及原因

没有改 Tick、dispatch、internal/loop、internal/classify、internal/route、internal/runner、TestS23 及其夹具、design.md、goals.md。没有安装 skill，没有跑真实执行器，没有写 ~/.cardex。交接只有 S23。

## 接口请求

v2 没有单次调用执行器，Channel 没有 classifier 槽。launchCard 只按 id classifier 取渠道名，Runner.Classify 在 New 里保持 nil，分级渠道不会调用模型。请给 Channel 加分级槽并提供一次调用，不要在 runner 里新写。
TestSignalGuard 源码扫描退出码 1。写域外命中 cmd/cardex/cli_surface_test.go:1216、1222、1225、1226、1237，internal/host/host_test.go:170、429，internal/host/run.go:497（ws.Signal() 只读 WaitStatus）。不改这些文件，扫描仍匹配 `.Signal(`。
本环境 `/usr/bin/sandbox-exec` 退出码 71：`sandbox_apply: Operation not permitted`。`go build ./...` 与 `go vet ./...` 为 0，`go test ./...` 退出 1。TestFaults（faults_test.go:84）、TestS25（s25_flow_test.go:132）、TestS01、TestS02、TestS04、TestS05、TestS20、TestS23、TestRankingDefaultEntry、TestTickDispatchClaimsAndSecondTick（loop_test.go:65 的 ErrorExcerpt）、TestTickManualMergesWithoutAutoCards（loop_test.go:152，dev 卡 no_output；design-auto 由测试在 loop_test.go:123 建成 needs_input）以及 internal/host 里启动执行器的测试，都是执行器没跑起来。TestS24（scenarios_test.go:1911）的失败文本是 inside.txt 不存在，因为配置文件还没套上。需改 internal/sandbox/wrap_darwin.go:15 与 internal/host/run.go:158。二者不在本写域。断言未放宽。套件没有通过。
实测摘录：TestTickManualMergesWithoutAutoCards 尝试 ae2174a84fc392033 exit=71 outcome=no_output，excerpt 与 output.log 均为 "sandbox-exec: sandbox_apply: Operation not permitted"。TestS23 尝试 a07fefed5d7d1c0c7 exit=71，excerpt 与 output.log 同句。TestRankingDefaultEntry/no_config_file 尝试 a6dbbc69feefab073 exit=71，excerpt 与 output.log 同句。

## 已知问题

无 BUG skip。N1 问题选项、N2 decision 与 otto、X1 skill 定位提案措辞留给后续跟进，本次不改指南和 skill。

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
