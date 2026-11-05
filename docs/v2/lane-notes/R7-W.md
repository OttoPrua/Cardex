# R7-W

## 做了什么
sched.New 的默认 Engine 调用 loop.New(db, workspace.Git, 投递器).Step，也就是 internal/loop/design.go 的设计步，然后 DevStep。调用前把 CARDEX_HOME 设成这次 serve 解析的 home，返回后恢复。设计期错误写一条 design_error，再用已有的 loop.Dispose 做 §5 处置；Tick 不因该错误停下。没有可处置的尝试时只记事件。
runner.Lookup 补上 grok、cursor、kimi、agy。requires 含 goal_mode 时，host 在 Build 前设置 Grok 的 GoalMode、Prompt、SessionDir，Wait 之后调用 ConsumeGrokProcessExit。
未注入 Notify 时走 notify.New(notify.CommandExec)。Fallback 取目标 origin 与卡 origin 事件的差异，SubscriptionID 取渠道 SourceGroup。失败写 delivery_error，下一拍重试。
runServe 调用 sched.Serve。cardex host 与 CARDEX_SCHED_HOST 子进程都用 workspace.Git。schedule run 把模板 requires 抄到卡上。
空 tier 走 productionClassify：已注册执行器上 host.Run 一次，30s 超时仍由 classify.Classify 包住。失败写一条 classify_error 并保留种类默认 tier。已有 tier 不调用。

## 没做什么，以及原因
没改 DesignStep。internal/loop/design.go 和 TestDesignStepNeedsStoreAndDevStaysOpen 不在写域，该测试要求它返回 ErrNotImplemented。
没加 Head、CompareCommits、model 字段或 config.json 键。Fallback 和 SubscriptionID 用调用方已有的 origin 与 SourceGroup。
没删 CARDEX_SCHED_HOST。host 子命令已直接用 Git，serve 拉起的子进程仍靠这个变量进入 host.Run。

## 接口请求
R2-A Lookup：internal/runner/registry.go 返回已有的 Grok、Cursor、Kimi、AGY。
R2-B goal 模式：internal/host/run.go 在 Wait 后调用 runner.ConsumeGrokProcessExit。
R3-A 设计：sched.New 的 productionEngine 调 loop.New.Step，不经过 DesignStep。
R3-C：cmd/cardex/main.go 的 runServe 调 sched.Serve；cmdHost 用 workspace.Git；launch.go 的子进程 init 本来就是 Git。
R3-D：notify.New 加 notify.CommandExec；sched.deliveryMessage 填 Fallback 和 SubscriptionID。
R4-D：sched.productionClassify 经 host.Run，超时用 classify.Classify 已有的 30s。
仍未处理：DesignStep 仍是 ErrNotImplemented。Channel 没有分类器槽，只认 id classifier。workspace.Git 没有 Head 和 CompareCommits。设计失败若发生在尝试存在之前，没有可处置的尝试。

## 已知问题
Launch 未注入时，没有脚本的 fake 设计族卡和 review 卡留在 ready，避免生产 Serve 在脚本落地前被 §5 开问题。盘点研究卡即使 Launch 已被包装也不启动。注入 Launch 的 review、design、plan、replan 仍会启动。真执行器、卡 body 里的脚本、以及 scripts/<id>.json 仍会启动。
hermes 没有 ack 时，inbox 已写上，重试仍会再记一条 delivery_error。这是现有 notify 适配器的返回值。
