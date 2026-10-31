# R3-B

## 做了什么
`devStep` 是导出的 `DevStep`。一次调用处置该目标已经收割的尝试。同一存储状态上的第二次调用不再新建卡、问题、通知，也不再改渠道。数据库是 `$CARDEX_HOME/cardex.db`。

`dev`、`skeleton`、`repair`、`rebase`、`backlog` 的 `passed` 派一张复核卡，作者 family 交给现有 `route.Select` 排除。设计、研究、原型卡的 `passed` 不派复核。`tests_failed` 沿卡分支返工，计数再增加就会超过上限（默认 2，可配置）时改为派问题。`no_output` 按 `route.Select` 换一个渠道，第二次派问题。`result_unparsable` 派整理卡；整理仍失败则原卡进入 `needs_input`。`quota_hit` 调用 `quota.Ledger.ObserveLimit`，写入 `quota_block`，并从卡分支换渠道续做。`auth_failed` 把渠道健康标成 `auth_failed`，用 file 适配器按 origin 投递一条通知，再换渠道。`transport` 和 `timeout` 在同一渠道最多重试 2 次，然后换渠道。`refusal` 与 `permission_denied` 派问题，后者的正文包含被拒绝的操作。`budget_limited` 只在还没续做过、且硬预算未设或仍有余量时续做一次，再次出现就派问题。`violation` 作废该次尝试、留下分支并派问题。

plan、review、normalize、replan 的 `passed` 在同一次调用里吸收。卡清单按宽松 JSON 解析：缺字段用默认值，无效依赖记 `invalid_dep`，依赖成环只断开一条边并在节点上挂开放问题，节点范围外的卡变成设计问题。复核 pass 把原卡推进到 `merge_queued`，rework 把意见写入 `ReworkNote`。整理成功把提取结果写回原卡的结果文件和正文，不自动派复核。

只有 `mode=auto` 会在同一次拆卡全部终态、节点重开或熔断之后派一张 replan；未结束的 replan，或这次重开/熔断已经被记下，就不再派，新的触发才再派。吸收 replan 只增删改本节点上尚未开始的卡。新清单卡使用新的 id，同一清单里的依赖改写成该 id。熔断暂停该节点里还没终态的卡，不暂停 `running` 和 replan，并派一个 breaker 问题，file 适配器的目的地是 origin。软预算到 80% 记一条提醒；硬预算暂停目标并派问题。预算未设的卡按 kind 填默认值，research 是 design 的四分之一，`$CARDEX_HOME/config.json` 可以替换这些默认值。三个完成条件同时成立才进入 `done_candidate` 并派 done 问题。批准后只在本地把集成分支合进项目主分支，不 push。

## 没做什么，以及原因
设计期、`DesignStep`、研究卡事实流和冻结是 R3-A。合并队列、派发、`sched.Loop`、宿主启动和声明冲突等待是 R3-C。prompt 模板和真正的 notify 包是 R3-D。S09 与 S15 使用 loop 内的 file 通知，不调用 `notify.Stub` 或 `question.Stub`。没有改 `internal/model`，也没有做骨架卡门禁、认证失败后的渠道恢复、backlog、看板、成绩单、真实 CLI、`~/.cardex` 或 push。

## 接口请求
R3-C 调用 `loop.DevStep(ctx, objectiveID)`，进程需已设置 `CARDEX_HOME`。通知用 `loop.WithNotifier` 注入；未注入时写到 `$CARDEX_HOME/notifications`。配额封锁用 `loop.RestoreQuota` 重放。自动换渠道把选出的 channel 写进卡，model 只记在 `auto_route`。`route.Select` 仍把非空 Channel 当成显式 pin 并跳过红线。派发若再次选择：卡 channel 等于最近一条 `auto_route.channel` 时要清掉；该事件的 `pin` 非空才是用户 pin，应放回请求，`exclude` 是失败渠道。`route.Request` 仍没有 Exclude 字段。不需要新的 model 字段。

定位表为空时，本 lane 只在当次请求里用各渠道自己的 tier 和模型造候选，不写入定位表，也不把卡的 tier 盖到每个模型上。

## 转交
无。

## 已知问题
自动模式下相同错误签名出现两次会熔断。传输、超时、额度和认证如果摘录相同，也会计入这个签名。连续失败那一条不计这些基础设施结果。

## 修复记录
产出代码的开发类卡 passed 后必须进入复核，不能只记 dev_disposed → internal/loop/outcome.go:72、internal/loop/dev_apply.go:196 → TestPassedCardsEnterReview
自动换渠道必须走既有路由筛选并排除刚失败的渠道，不能按渠道 id 和第一个模型来选 → internal/loop/dev_apply.go:280、internal/loop/dev_apply.go:300 → TestAutomaticReassignmentUsesRoute
拆卡清单的默认 key、重复 key 和跨节点相同 key 必须变成不冲突的卡 id，同清单依赖改写成新 id → internal/loop/outcome.go:442、internal/loop/dev_apply.go:582、internal/loop/dev_apply.go:715 → TestListKeysDoNotCollide
budget_limited 的自动续做只限一次，再次出现即使硬预算未设或仍有余量也派问题 → internal/loop/outcome.go:120、internal/loop/dev_apply.go:180 → TestBudgetLimitedContinuesOnce
同一次重开或熔断只派一张 replan，结束后不因节点仍重开或同一批尝试再次派卡 → internal/loop/dev_apply.go:846、internal/loop/dev_apply.go:919 → TestReplanTriggerConsumed
自动换渠道必须计入其他目标的占用和去重后的窗口配额，空定位表不能发明候选，选出的渠道不能写成显式 pin → internal/loop/dev_route.go:19、internal/loop/dev_route.go:144、internal/loop/dev_route.go:195、internal/loop/dev_apply.go:295、internal/loop/dev_apply.go:310 → TestAutomaticReassignmentUsesRoute、TestAutomaticRouteIsNotAnExplicitPin
熔断 replan 的消费键必须钉在触发它的证据上，吸收该 replan 的尝试后旧的重复签名和停滞都不能再次派卡 → internal/loop/dev_apply.go:874、internal/loop/dev_apply.go:918、internal/loop/dev_apply.go:982 → TestBreakerReplanAbsorbed
换渠道必须把选出的渠道写入卡并标为 ready；定位表为空时用各渠道自己的 tier 和模型作未保存候选，自动写入的渠道在下次路由请求里不是用户 pin → internal/loop/dev_apply.go:292、internal/loop/dev_route.go:66、internal/loop/dev_route.go:171 → TestS07
换渠道必须把选出的渠道写入卡并标为 ready；定位表为空时用各渠道自己的 tier 和模型作未保存候选，自动写入的渠道在下次路由请求里不是用户 pin → internal/loop/dev_apply.go:292、internal/loop/dev_route.go:66、internal/loop/dev_route.go:171 → TestS08
换渠道必须把选出的渠道写入卡并标为 ready；定位表为空时用各渠道自己的 tier 和模型作未保存候选，自动写入的渠道在下次路由请求里不是用户 pin → internal/loop/dev_apply.go:292、internal/loop/dev_route.go:66、internal/loop/dev_route.go:171 → TestS09
