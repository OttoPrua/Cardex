# R3-B

## 做了什么
`devStep` 是导出的 `DevStep`。一次调用处置该目标已经收割的尝试。同一存储状态上的第二次调用不再新建卡、问题、通知，也不再改渠道。数据库是 `$CARDEX_HOME/cardex.db`。

开发卡 `passed` 派一张复核卡，作者 family 交给现有 `route.Select` 排除。`tests_failed` 沿卡分支返工，计数再增加就会超过上限（默认 2，可配置）时改为派问题。`no_output` 换一个渠道，第二次派问题。`result_unparsable` 派整理卡；整理仍失败则原卡进入 `needs_input`。`quota_hit` 调用 `quota.Ledger.ObserveLimit`，写入 `quota_block`，并从卡分支换渠道续做。`auth_failed` 把渠道健康标成 `auth_failed`，用 file 适配器按 origin 投递一条通知，再换渠道。`transport` 和 `timeout` 在同一渠道最多重试 2 次，然后换渠道。`refusal` 与 `permission_denied` 派问题，后者的正文包含被拒绝的操作。`budget_limited` 在目标硬预算还有余量时续做一次，否则派问题。`violation` 作废该次尝试、留下分支并派问题。

plan、review、normalize、replan 的 `passed` 在同一次调用里吸收。卡清单按宽松 JSON 解析：缺字段用默认值，无效依赖记 `invalid_dep`，依赖成环只断开一条边并在节点上挂开放问题，节点范围外的卡变成设计问题。复核 pass 把原卡推进到 `merge_queued`，rework 把意见写入 `ReworkNote`。整理成功把提取结果写回原卡的结果文件和正文，不自动派复核。

只有 `mode=auto` 会在同一次拆卡全部终态、节点重开或熔断之后派一张 replan；已经有未结束的 replan 就不再派。吸收 replan 只增删改本节点上尚未开始的卡。熔断暂停该节点里还没终态的卡，不暂停 `running` 和 replan，并派一个 breaker 问题，file 适配器的目的地是 origin。软预算到 80% 记一条提醒；硬预算暂停目标并派问题。预算未设的卡按 kind 填默认值，research 是 design 的四分之一，`$CARDEX_HOME/config.json` 可以替换这些默认值。三个完成条件同时成立才进入 `done_candidate` 并派 done 问题。批准后只在本地把集成分支合进项目主分支，不 push。

## 没做什么，以及原因
设计期、`DesignStep`、研究卡事实流和冻结是 R3-A。合并队列、派发、`sched.Loop`、宿主启动和声明冲突等待是 R3-C。prompt 模板和真正的 notify 包是 R3-D。S09 与 S15 使用 loop 内的 file 通知，不调用 `notify.Stub` 或 `question.Stub`。没有改 `internal/model`，也没有做骨架卡门禁、认证失败后的渠道恢复、backlog、看板、成绩单、真实 CLI、`~/.cardex` 或 push。

## 接口请求
R3-C 调用 `loop.DevStep(ctx, objectiveID)`，进程需已设置 `CARDEX_HOME`。通知用 `loop.WithNotifier` 注入；未注入时写到 `$CARDEX_HOME/notifications`。配额封锁用 `loop.RestoreQuota` 重放。这次不需要新的 model 字段。

## 已知问题
自动模式下相同错误签名出现两次会熔断。传输、超时、额度和认证如果摘录相同，也会计入这个签名。连续失败那一条不计这些基础设施结果。
