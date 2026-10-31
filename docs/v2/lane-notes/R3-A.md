# R3-A

## 做了什么
`question.Service` 的 Create 保存节点或卡的 scope 和 kind。Answer 记下所选选项；空答案不落库，也不解除阻塞。Pending(scope) 只返回该 scope 仍未回答的问题。Blocked 仅在同一 scope 还有未回答问题时为真。Create 调用 `notify.Deliverer`，本 lane 没有适配器。

`loop.New(store, workspace, deliverer).Step` 只跑设计期，入口函数是 `designStep`。`DesignStep(ctx, objectiveID)` 仍可调用；没有 store 时返回错误。`DevStep` 仍返回未实现，Step 不调用它。`mode=auto` 先派一张盘点 research 卡，终态后才建 direction 节点和设计卡。吸收设计卡会增删开放问题（同一节点上相同正文只存一次）、创建 module 或 detail 子节点并派设计卡，并可记录可冻结。没有非 otto 的 open 或 researching 问题时，提议过的节点才变为 freezable。冻结按层 × 模式：direction 在 safe 和 fast 都等冻结问题；module 在 safe 等回答，在 fast 直接冻结；detail 在 fast 直接冻结，在 safe 等本节点的复核卡通过（这里只建卡）。已冻结节点不随 freeze_mode 改变，尚未冻结的会变。冻结后派 plan 卡。spike（含 `[experiment]`）停在 done，结论回填到对应开放问题。开发卡或复核卡的 `## 设计问题` 建立 reopen 问题；同意之后节点才 reopened，未开始的卡变为 paused，运行中的卡保持运行，并派 replan。

开放问题解析 `[fact]`、`[fact web]`、`[experiment]`、`[decision]`、`[otto]`，缺标签视为 decision。`[fact web]` 类型仍是 fact，原文保留标签，单独成卡且 requires 含 web。open 的 fact 每张研究卡最多 5 个，派发后变为 researching。节点上还有未完成的研究卡时，不派新的设计、拆卡或重规划卡。`## 事实` 下的 `### Q<id>` 写入 `model.OpenQuestion`：commit 是派发时的集成分支 HEAD，cited_paths 从证据提取，缺证据则把握为低，没有对应块则为 unanswered。研究卡不进入合并队列。下一张卡之前，cited_paths 自 commit 以来有改动的 fact 标为 stale，比较调用 `workspace.Manager.Diff`。`[fact] Q<id> 复查：` 升一级 tier 且 asks 加 1，第二次改为 decision。下一张设计、拆卡或重规划卡附上该节点的 fact 列表（含目标 Q0），unanswered 标 `未能核实`，stale 标 `可能过期`。`mode=manual` 不创建这些卡和节点。

## 没做什么，以及原因
未做开发期、结果表、熔断、预算和 done_candidate，这些属于 R3-B。未做合并队列、`sched.Loop.Tick`、派发和 `cardex serve`，这些属于 R3-C。未写设计卡或研究卡的前言模板，渲染属于 R3-D。未做通知适配器，也没有看板或 CLI 上的事实展示，stale 不会自动再研。未改 `internal/model`。

## 接口请求
`workspace.Manager` 增加 `Head(ctx, repo, ref string) (string, error)`。ref 为空时返回当前 checkout 的 HEAD。stale 要拿派发时记下的提交和当前集成分支 HEAD 做比较。现有 `Diff(worktree, base)` 只给出工作区相对一个 base 的路径，不能返回 HEAD，也不是两个提交之间的范围。最小改法是只加 Head，不改 Diff 的含义。

## 已知问题
Head 落地前，派发时的提交从仓库 `.git` 的 HEAD、loose ref 或 packed-refs 读取；路径是否变化仍只调用 `Manager.Diff`。`DesignStep` 没有 store，直接调用会因空依赖报错，实际入口是 `loop.New(...).Step`。调度仍是 stub，场景把终态写进卡正文后再调用 Step。
