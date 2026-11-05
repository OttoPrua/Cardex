# R7-W

## 做了什么
sched.New 的 Engine 调用 loop.New(db, workspace.Git, 投递器).Step 再 DevStep，并把 CARDEX_HOME 设成这次 serve 的 home。设计错误写 design_error。只有错误文本点名、且已有真实退出码的设计族尝试才走 harvest 和 loop.Dispose。仍在运行的尝试保持运行，不虚构退出码。
requires 含 goal_mode 时，host 把 SessionDir 设成这次 CLI 写入的会话目录。会话变为 complete 或 budget_limited 后只经 SignalGroup(SIGTERM) 结束，并保留该终态。internal/runner/grok.go 仅在 SessionID 非空时为 goal 追加 -s。参数冲突留给 R7-INT。
分级先按执行器抽出最终回答，再交给已有的行首 TIER/REASON 解析。非零退出、错误类或超时退回种类默认并记 classify_error。收尾用未过期的 context。卡 id 前缀 cls- 的尝试不进入普通收割。
通知的 Home、Origin、Fallback、SubscriptionID 经同一个 fillDelivery。SubscriptionID 保持空。Fallback 只读本次 home 的 config.json 顶层 notify.fallback。卡 origin 优先，否则用目标 origin。hermes 缺订阅 id 时记 delivery_skip，原因含「缺订阅 id」，不写 hermes-inbox，也不记 delivery_error 或 design_error。inbox 和已配置的后备照常投递。非法 fallback 记一条 notify_warning 且不跳转。
runServe 调用 sched.Serve。schedule 把模板 requires 抄到卡上。空 tier 走 productionClassify。30 秒超时仍由 classify.Classify 包住。

## 没做什么，以及原因
没改 DesignStep。internal/loop/design.go 和 TestDesignStepNeedsStoreAndDevStaysOpen 不在写域，该导出仍返回 ErrNotImplemented。
没做 N1 分级缓存、N2 跳过降级分类器、N3 workspace.Git 的 Head 与 CompareCommits、X1 去掉无脚本 ready 等待。没改 design.md。没新增命令或配置键。notify.fallback 只按既有口径消费。没把 notify 加入 knownTop，未知键只产生警告，配置仍被读取。
没删 CARDEX_SCHED_HOST。没推断订阅 id。

## 接口请求
R2-A Lookup：internal/runner/registry.go 返回已有的 Grok、Cursor、Kimi、AGY。
R2-B goal：internal/host/run.go 观察 runner.GrokGoalSessionDir。grok.go 的 -s 只在 goal 且 SessionID 非空时出现。与 R2-B 的参数冲突留给 R7-INT。
R3-A 设计：productionEngine 调 loop.New.Step，不经过 DesignStep。
R3-C：cmd/cardex/main.go 的 runServe 调 sched.Serve；cmdHost 用 workspace.Git。
R3-D：notify.New 使用 notify.CommandExec。投递字段由 sched.fillDelivery 填入，不改 internal/notify/deliver.go。
R4-D：productionClassify 经 host.Run。超时仍是 classify 已有的 30 秒。分类器槽仍只认 id classifier。

## 已知问题
Launch 未注入时，没有脚本的 fake 设计族卡和 review 卡留在 ready。盘点研究卡不启动。真执行器、卡 body 里的脚本，以及 scripts/<id>.json 会启动。
DesignStep 仍是 ErrNotImplemented。workspace.Git 没有 Head 和 CompareCommits。
修复记录：B1 设计错误只处置已退出且被点名的设计族尝试 → internal/sched/engine.go:26,136 → TestDesignErrorLeavesRunningSibling, TestDesignErrorDisposesNamedExit
修复记录：B2 goal 终态绑定本次会话并用 SignalGroup 结束 → internal/host/run.go:76,275 internal/runner/grok.go:103,178 internal/runner/api.go:25 → TestGoalTerminalStaysOpenUntilSignaled
修复记录：B3 分级使用执行器最终回答 → internal/sched/classify_prod.go:162 → TestClassifyRunnerSamplesKeepTier
修复记录：B4 分级失败退回默认且 cls- 不进收割 → internal/sched/classify_prod.go:49,137,144 internal/sched/loop.go:163 → TestClassifyNonzeroExitUsesKindDefault, TestClassifyTimeoutThenNextTick
修复记录：B5 通知不取 SourceGroup，后备只读 notify.fallback，缺订阅 id 记跳过 → internal/sched/deliver.go:33,75,142 internal/sched/engine.go:66 internal/sched/loop.go:1016 → TestDeliveryFieldsSameAcrossPaths, TestHermesSkipAndFallback, TestCardOriginPreferredOverTarget
