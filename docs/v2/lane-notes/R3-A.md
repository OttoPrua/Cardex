# R3-A

## 做了什么
`question.Service` 的 Create 保存节点或卡的 scope 和 kind。Answer 记下所选选项；空答案不落库，也不解除阻塞。Pending(scope) 只返回该 scope 仍未回答的问题。Blocked 仅在同一 scope 还有未回答问题时为真。Create 调用 `notify.Deliverer`，本 lane 没有适配器。

`loop.New(store, workspace, deliverer).Step` 只跑设计期，入口函数是 `designStep`。`DesignStep` 和 `DevStep` 都返回 `ErrNotImplemented`，Step 不调用 DevStep。`AbsorbDisposed` 用本次尝试的 id、outcome 和收割原文吸收盘点、design、plan、replan、research、spike，以及开发卡或复核卡的 `## 设计问题`；调用前卡不必是终态。没有节点文档或上下文包改动、且不是 `merge_queued` 或 `merged` 的 design、plan、replan 通过后变为 done；已是这两种状态的保持原状，带文档或上下文包改动的不标 done，未合入前不派该节点的下一张设计、拆卡或重规划。终态卡若当前尝试自己还有结果文件，仍吸收该文件。`mode=auto` 的盘点经这次吸收后再 `Step`，才建 direction 节点和设计卡。吸收按尝试而不是按卡：传入的原文，或终态路径上当前尝试的 `.cardex-result.md`，不回退更早的尝试。`transport`、失败、取消和 `violation` 不写事实或 spike 答案、不标 done、不增删开放问题、不建子节点、不记可冻结或复核通过；同卡上后来的 `passed` 仍生效，同一尝试不重复施加。缺失的 fact 保持 unanswered。design、plan、replan 为空或 `result_unparsable` 时只建一张整理卡，`Deps[0]` 是源卡；整理卡未结束不标 `needs_input`、不消费，失败后源卡才 `needs_input`，通过且提取可用则吸收到源卡。卡正文仍是任务。吸收设计卡会增删开放问题（同一节点上相同正文只存一次）、创建 module 或 detail 子节点并派设计卡，并可记录可冻结。没有非 otto 的 open 或 researching 问题时，提议过的节点才变为 freezable。冻结按层 × 模式：direction 在 safe 和 fast 都等冻结问题；module 在 safe 等回答，在 fast 直接冻结；detail 在 fast 直接冻结，在 safe 等本节点的复核卡通过（`Deps[0]` 是当前设计卡，这里只建卡）。已冻结节点不随 freeze_mode 改变，尚未冻结的会变。冻结后派 plan 卡。spike（含 `[experiment]`）停在 done，结论回填到对应开放问题。开发卡或复核卡的 `## 设计问题` 建立 reopen 问题；同意之后节点才 reopened，未开始的卡变为 paused，运行中的卡保持运行，并派 replan。

开放问题解析 `[fact]`、`[fact web]`、`[experiment]`、`[decision]`、`[otto]`，缺标签视为 decision。`[fact web]` 类型仍是 fact，原文保留标签，单独成卡且 requires 含 web。open 的 fact 每张研究卡最多 5 个，派发后变为 researching。节点上还有未完成的研究卡时，不派新的设计、拆卡或重规划卡。吸收看到的 `## 事实` 来自本次尝试的结果，`commit` 是该尝试的 `BaseCommit`，cited_paths 从证据提取，缺证据则把握为低，没有对应块则为 unanswered。研究卡不进入合并队列。下一张卡之前，若 workspace 同时提供 `Head` 和 `CompareCommits`，cited_paths 在该提交与集成分支 HEAD 之间有差异的 fact 标为 stale。`[fact] Q<id> 复查：` 升一级 tier 且 asks 加 1，第二次改为 decision。下一张设计、拆卡或重规划卡附上该节点的 fact 列表（含目标 Q0），unanswered 标 `未能核实`，stale 标 `可能过期`。冻结节点上每个终态研究尝试都已处理或跳过之后，才派一张带 `## 事实摘要` 的拆卡，摘要里是各卡已经吸收的结论；取消或失败的最后一次收割也建这张卡，且不写入该次作废结果。这张卡存在之后才把研究完成记为已续派；收割先跑过时，之后的 `Step` 仍给出这张拆卡，且通过的收割把研究卡写成 done。`mode=manual` 下 `Step` 和收割都不创建 direction、module、detail，也不创建 design、plan、replan、research、spike，包括设计原文里的子节点和 `[experiment]`；复核通过、不建卡的设计问题，以及整理卡，照常处理。

## 没做什么，以及原因
未做开发期、结果表、熔断、预算和 done_candidate，这些属于 R3-B。未做合并队列、`sched.Loop.Tick`、派发和 `cardex serve`，这些属于 R3-C。未写设计卡或研究卡的前言模板，渲染属于 R3-D。未做通知适配器，也没有看板或 CLI 上的事实展示，stale 不会自动再研。未改 `internal/model`。

## 接口请求
`workspace.Manager` 增加 `Head(ctx, repo, ref string) (string, error)` 和 `CompareCommits(ctx, repo, from, to string) (Diff, error)`。ref 是集成分支。CompareCommits 返回两个提交之间有差异的路径，不改 `Diff` 的工作区含义。设计期只在具体类型同时具备这两个方法时，用派发尝试的 `BaseCommit` 和当前集成分支 HEAD 比较。集成者把 `sched.Runner.Dispose` 接到 `loop.AbsorbDisposed`：删除 worktree 之前调用 `AbsorbDisposed(ctx, Deps{Store, WS, Notify}, attemptID, res.Outcome, res.ResultText)`，attemptID 必须是这一次尝试。`deliver` 在回调返回后不得 `UpdateCard` 回调前的那份 `*Card`：今天 `Dispose` 是 `(ctx, *Card, harvest.Result)`，`deliver` 随后保存原指针，会把吸收刚写入的 done 盖回去。收割到的 claims 要先写进库里的卡再调用吸收。design、plan、replan 若改了节点文档或 `<design_dir>/<objective>/context.md`，集成者将其入队为 `merge_queued`；本 lane 不写文件、不跑合并队列，队列只合并 `merge_queued`。整理卡的 `Deps[0]` 是源卡。`executionDepsDone` 目前只对 review、repair、rebase 跳过这条依赖；normalize 也要同样跳过，这样源卡尚未终态时整理卡仍会被派发。`DesignStep` 保持 `ErrNotImplemented`；设计期用 `loop.New(store, workspace, deliverer).Step`。集成者改 `internal/loop/api.go` 和调度默认，本 lane 不改 scheduler 或合并。safe/detail 复核卡的 `Deps[0]` 是被冻结的设计卡，这是节点冻结复核，不得当成开发卡通过去把该设计卡送进合并队列。

## 已知问题
`workspace.Git` 还没有上述两个方法，接口落地前不会把事实标为 stale。生产 `Dispose` 仍是 nil，要等集成者接到 `AbsorbDisposed`。

## 修复记录
产出吸收解析尝试 worktree 的 `.cardex-result.md`，卡正文只留任务 → `internal/loop/research.go:197` `design.go:114` `design.go:140` `design.go:167` `design.go:213` `design.go:255` `research.go:117` → `TestAbsorbResultFileNotCardBody`
未收敛、没有活动设计/拆卡/重规划、没有未完成研究或未回答人工决定的节点按阶段收到下一张卡 → `internal/loop/design.go:340` `design.go:369` → `TestUnconvergedNodeKeepsMoving`
重开后的 replan 提议可冻结才重新进入可冻结，本次同意被消费，已完成的历史 replan 不阻挡，进行中的 replan 阻挡，旧冻结确认不支配新一轮 → `internal/loop/design.go:554` `design.go:621` `design.go:925` → `TestReopenRoundConsumesConsent`
事实版本用派发尝试的 BaseCommit，经 Head 与 CompareCommits 和集成分支 HEAD 比较 → `internal/loop/research.go:117` `research.go:150` `research.go:177` → `TestStaleUsesAttemptBaseAgainstIntegrationHead`
设计期创建的 direction、module、detail 写入 `<design_dir>/<objective>/<node>.md` → `internal/loop/design.go:334` `design.go:662` `design.go:1051` → `TestCreatedNodesHaveDocPath`
safe/detail 复核卡绑定当前待冻结设计并携带 AuthorFamily，只有这张复核通过才冻结 → `internal/loop/design.go:270` `design.go:530` `design.go:1018` → `TestReviewTracksCurrentDesign`
可注入依赖的设计期入口是 `loop.New(...).Step`，空依赖 `DesignStep` 交给集成者 → `internal/loop/design.go:28` `design.go:36` → `TestDesignStepNeedsStoreAndDevStaysOpen`
B1 收割原文在清理前按本次尝试吸收，盘点不必先变成终态就能进入 direction；通过且可用的 design、plan、replan 吸收后变为 done，研究结束后的下一张设计卡和可冻结的 replan 才能继续 → `internal/loop/design.go:1082` `design.go:1126` `design.go:1199` `research.go:197` → `TestAbsorbDisposedHarvest` `TestAbsorbDisposedUnblocksNext`
B6 safe/detail 复核卡 Deps[0] 指向当前设计卡，AuthorFamily 取该设计，非本版本通过不冻结 → `internal/loop/design.go:534` → `TestReviewTracksCurrentDesign`
B7 撤回 api.go，DesignStep 与 DevStep 保持 ErrNotImplemented，设计期走 New 的 Step → `internal/loop/api.go:24` `design.go:32` → `TestDesignStepNeedsStoreAndDevStaysOpen`
B8 只读被处置的尝试；缺失或不完整的设计改为整理或 needs_input；失败、取消、violation 不记冻结通过 → `internal/loop/research.go:218` `design.go:1139` `design.go:1152` `design.go:273` `design_parse.go:526` → `TestAbsorbBindsDisposedAttempt`
B1 冻结节点的研究全部结束且每个终态尝试已处理或跳过后才建一张带事实摘要的拆卡，作废的最后一次收割也建这张卡且不写入作废结果 → `internal/loop/design.go:54` `design.go:1325` `design.go:1381` `design.go:1409` `design.go:1461` `research.go:130` `research.go:136` `design_parse.go:13` → `TestFrozenResearchSuccessorPlan`
B8 消费按尝试而不是按卡；transport、失败、取消、violation 不产生成功副作用，后来的 passed 仍生效，同一尝试不重复施加 → `internal/loop/design.go:1231` `design.go:1311` `design_parse.go:527` → `TestAttemptScopedConsumption`
B9 可解析的 design、plan、replan 不等于已合入；merge_queued 和 merged 保持不变，节点文档或上下文包未合入时不标 done、不派下一张 → `internal/loop/design.go:363` `design.go:1196` `design.go:1524` → `TestParsedOutputWaitsForMerge`
B10 空或 result_unparsable 的 design、plan、replan 只建一张整理卡；未结束不标 needs_input、不消费，失败后才 needs_input，通过的提取吸收到源卡 → `internal/loop/design.go:1143` `design.go:1342` → `TestNormalizeRecovery`
B11 mode=manual 时收割和 Step 都不创建设计期节点或 design、plan、replan、research、spike 卡 → `internal/loop/research.go:37` `design.go:286` `design.go:311` `design.go:645` `design.go:696` `design.go:734` `design.go:774` → `TestManualHarvestRefusesDesignPhase`
B1 已被后继 plan 消费的历史研究不得再派第二张 plan；创建该 plan 时已结束的研究记为已消费，进行中的 plan 也算后继已经存在 → `internal/loop/design.go:366` `design.go:640` `design.go:791` `design.go:1559` `design.go:1612` → `TestHistoricalResearchDoesNotDispatchAnotherPlan`
B8 两个吸收入口都先看当前尝试，卡级 loop.absorbed 不能封死同卡的后续尝试 → `internal/loop/design.go:99` `design.go:1189` `design.go:1428` → `TestCurrentAttemptNotSealedByCardAbsorb`
B9 依赖设计文档的后继 plan 只在文档合入之后生成；每条冻结入口都等这次合入，未改文档则立刻派 → `internal/loop/design.go:358` `design.go:643` `design.go:1560` → `TestFreezeWaitsForMergedDesign`
B10 只收割一次整理卡就把结果写回仍处于 harvesting 的 Deps[0] 源卡；通过施加设计效果，失败、取消或不可用则 needs_input，不重收源尝试、不把源卡预设为终态 → `internal/loop/design.go:1237` `design.go:1443` `design.go:1499` → `TestNormalizeHarvestRestoresSource`
B12 研究真正终态且没有有效答案时仍记下 unanswered（盘点为 Q0）并不写入作废文本，随后继续推进 → `internal/loop/design.go:1206` `design.go:1438` → `TestTerminalResearchWithoutAnswerContinues`
B10 整理成功后按普通吸收收尾源卡：无文档改动置 done，merge_queued 与 merged 保持，并消费源尝试且不回读作废原文；失败、取消或不可用则 needs_input 且不写入 → `internal/loop/design.go:1274` `design.go:1280` `design.go:1358` → `TestNormalizeHarvestRestoresSource`
B13 研究卡未终态的跳过不封存 Q0、不结束 researching、不让已有可冻结生效；真正终态且无有效答案才记 unanswered 且不写作废文本，后来的 passed 覆盖空 Q0 → `internal/loop/design.go:104` `design.go:165` `design.go:761` `design.go:1315` `design.go:1503` `design.go:1512` → `TestResearchRetryWaitsForPassedAnswer` `TestTerminalResearchWithoutAnswerContinues`
终态 unanswered 回填对一次研究尝试幂等，只处置一次，不得覆盖后续复查的 researching → `internal/loop/design.go:1316` `design.go:1333` `design.go:1527` `design.go:1536` `design_parse.go:13` → `TestTerminalUnansweredBackfillIsOneShot`
