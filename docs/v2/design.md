# Cardex v2 设计

依据：
- 已锁定目标：`work/cardex-refactor-plan-20261030/goals.md`
- 事实核实：同目录的 `facts-*.md`、`inventory.md`、`goal-and-state.md`

本设计从目标和过去的失败出发，不继承 v1 的程序结构。v1 代码只作为素材来源，例如各 CLI 的调用参数、PTY 托管、错误文本分类。

## 1. 从过去的问题推出的原则

| 过去的问题（452 张 held/failed 的主因） | 根因 | v2 原则 |
| --- | --- | --- |
| 终局不明 153 张（事件流不完整、终局无效、零事件） | 成败依赖解析执行器的事件流 | **结果以工作区为准**：进程退出后，一律检查 diff、跑验收命令、读结果文件。事件流只作辅助 |
| 收割未过 55 张；产出或新卡不合流程就失败或暂停 | 过程合规被当作硬条件 | **宽进严出**：只有少数硬底线会阻断，其余自动补全、修正或标为待复核 |
| 集成门默认 held 53 张；人工封存 94 张 | 每一步合并都要人工放行 | **自动合并队列**：测试和复核通过就合入目标集成分支。人工门只留在目标级，即设计冻结、合入项目主分支和切换 |
| 锁太粗：未声明写域锁整个 worktree，目录声明盖住子文件，执行租约锁整个 `task.Dir` | 多张卡共用同一个目录，只能靠锁互斥 | **每次尝试一个独立 git worktree**。锁不再保证正确性，只作调度提示；正确性由合并队列保证 |
| 权限拒绝 28 张 | 执行器在运行中停下来等审批 | **worktree 内自动放行，用系统沙箱兜底**：可写范围只有 worktree、该 CLI 自己的目录和临时目录 |
| 认证 10、传输 7、额度相关 35 张 | 渠道状态在派发前未知；重试不安全，所以不敢重试 | **渠道健康和余量是派发前的输入**。工作区隔离后重试是安全的，失败的尝试作废或从它已提交的部分接着做 |
| `stale task write rejected` 等并发写冲突 | 多个进程各自读写 JSON 状态文件 | **单一 SQLite 数据库（WAL）**，事务写入，事件表只追加 |

## 2. 总体结构

- **单一二进制 `cardex`。**
  - `cardex serve` 是常驻进程，由 launchd 以 KeepAlive 方式拉起。它负责调度循环、合并队列、定时任务、通知投递，以及只读看板 HTTP 服务。某一轮出错时不退出，见第 6.1 节“单轮出错”。
  - 其余子命令直接读写数据库，与常驻进程并发安全。
- **尝试宿主。** 每次尝试由 `cardex host <attempt-id>` 子进程托管。它启动执行器（需要 PTY 时用 PTY），记录输出和用量；执行器退出后提交改动，并把结果写回数据库。宿主与 `serve` 解耦：`serve` 重启后通过数据库和 PID 对账，不打断正在跑的尝试。
- **存储。**
  - 数据库是 `$CARDEX_HOME/cardex.db`（SQLite，WAL），驱动用纯 Go 的 `modernc.org/sqlite`，不需要 cgo。这是 v2 唯一新增的第三方依赖。
  - 执行器原始输出放在 `$CARDEX_HOME/attempts/<id>/`。
  - 配置是 `$CARDEX_HOME/config.json`，只用标准库解析，未知键忽略并给出警告。
- **目录。**
  - 开发期：home 用项目根下的 `cache/cardex-v2-dev/home/`，看板端口 `127.0.0.1:8790`。
  - 切换后（2026-11-07 作者改定）：v2 直接接管 v1 的位置，home 是 `~/.cardex`，看板 `0.0.0.0:8788`，launchd 标签 `com.cardex.serve`。切换时 v1 的 `~/.cardex` 整个改名为 `~/.cardex-v1`，作为导入来源和回退依据，不删除；v2 在新建的 `~/.cardex` 里从导入开始。
  - home 解析顺序：`--home`，否则 `CARDEX_HOME`，否则 `~/.cardex`。v2 拒绝使用 v1 的数据目录（`tasks/` 里有 `.json` 文件且没有 `cardex.db`），报错时给出 `import-legacy` 的用法。v2 不保留任何端口。
  - 开发环境的保护放在开发工具上：lane、复核和测试用的 signal 沙箱禁止写 `~/.cardex` 和 `~/.cardex-v1`；影子运行在沙箱外，靠显式的 `CARDEX_HOME` 和脚本静态检查。
- **不在 v2 中的东西。** Gemini、claudego 兼容、旧 bootstrap 恢复入口、cross、assemble/plan/brief、一次性授权流程、旧 fallback 链、Owner 矩阵和 mixed routing、手工回执和 SHA 读回。

## 3. 数据模型

字段只列关键项，实现时可以补充，但不得改变语义。

- **Project（项目）**
  - `id`、`repo_path`（主 checkout）
  - `worktree_root`：默认 `<项目根>/worktrees/cardex/`。符合 Development 布局规则；项目没有项目根时退回 `repo_path/../worktrees/cardex/`。
  - `test_cmd`：默认的全量测试命令。
  - `design_dir`：默认 `docs/design/`。
  - `forbidden_paths`：可选，见第 13 节。
- **Objective（目标）**
  - `id`、`project`、`title`、`acceptance`：验收命令和检查清单，均为可选。
  - `budget`：`soft_tokens`、`hard_tokens`。
  - `priority`。
  - `mode`：`auto` 或 `manual`。
  - `freeze_mode`：`safe` 或 `fast`。
  - `origin`：创建者会话，见第 9 节。
  - `status`：`active`、`paused`、`done_candidate`、`done`、`canceled`。
  - `integration_branch`：`cardex/<objective>/main`，从项目主分支切出。记录里只存名字；git 引用不存在时，派发在第一次为该目标建 worktree 之前，从主分支当前 HEAD 创建它。主分支按 `main`、`master` 的顺序探测，与合入主分支时相同。创建是幂等的，导入的目标同样适用。两个主分支都不存在时，按 `launch_failed` 处置（第 5 节）。
- **DesignNode（设计节点）**
  - `id`、`objective`、`parent`。
  - `layer`：`direction`（目标方向）、`module`（模块划分）或 `detail`（模块内细节）。
  - `doc_path`：在集成分支里，位于 `<design_dir>/<objective>/<node>.md`。
  - `open_questions[]`。每条问题有 `id`、`text`、`type`、`status`、`asks`，以及答案字段 `answer`、`evidence`、`confidence`、`commit`、`cited_paths[]`。
    - `type` 是 `fact`、`experiment`、`decision` 或 `owner`。`owner` 表示只有使用者本人能定的事。改名前写入的个人名类型值仍按 `owner` 读取。
    - `status` 是 `open`、`researching`、`answered`、`stale` 或 `unanswered`。
    - 这些字段按第 6.6 节使用，由 R2-D 补进模型，R1 不要求。
  - `status`：`drafting`、`freezable`、`frozen`、`reopened`。
- **Card（卡）**
  - `id`、`objective`、`node`、`kind`（见第 6.3 节）、`title`、`body`。
  - `tier`：`T1`、`T2` 或 `T3`，可以为空，空时由入队分级填写。
  - `requires[]`：能力要求，例如 `web`、`goal_mode`。
  - `claims[]`：见第 4 节。
  - `deps[]`、`acceptance[]`（命令）。
  - `base`：起点。默认是集成分支 HEAD；返工和续做时是本卡分支。
  - `status`：`ready`、`waiting`（依赖、声明冲突或问题阻塞，附原因）、`running`、`harvesting`、`reviewing`、`merge_queued`、`merged`、`done`（不产出代码的卡）、`needs_input`、`paused`、`canceled`、`failed`。没有含义模糊的 held。
  - `rework_count`：唯一的返工计数。
  - `legacy`：从 v1 导入的原始状态和来源路径。
- **Attempt（尝试）**
  - `id`、`card`、`route`（channel 和 model，派发前确定并记录）、`route_reason`。
  - `worktree`、`branch`、`base_commit`、`head_commit`。
  - `pid`、`started_at`、`ended_at`、`exit`。
  - `usage`：tokens 及其来源和置信度。
  - `outcome`：见第 5 节。
  - `error_class`、`error_excerpt`。
  - 原始材料放在 `<home>/attempts/<id>/`，见第 5 节“留存”。
- **Channel（渠道）**
  - `id`、`runner`（grok、codex、claude、cursor、kimi、agy、opencode）。
  - `engine_profile`：只对 claude 有效，用于 engines 档案。
  - `source_group`：同一订阅或账号。
  - `models[]`。
  - `concurrency`。
  - `quota_windows[]`、`redline`（见第 7 节）。
  - `health`：`ok`、`auth_failed`、`degraded`，以及最近的错误。
- **Positioning（模型定位）**：每条是 `model → family、tiers[]、capabilities[]、preference`，带版本号，只能经提案审批后修改（见第 10 节）。
- **Question（待拍板）**
  - `id`、`objective`、`scope`（节点或卡）、`kind`（freeze、reopen、breaker、budget、refusal、done、custom）。
  - `text`、`options[]`、`answer`。
  - `deliveries[]`（投递记录）。
- **Event（事件）**：只追加，内容是 `ts`、`entity`、`type`、`data`。它替代 v1 的手工回执，看板和成绩单都从这里取数据。

## 4. 工作区、声明与并行

- **每次尝试一个 worktree。**
  - 路径：`<worktree_root>/<card>-<attempt>`。
  - 分支：`cardex/<objective>/c/<card>`。
  - 起点：新卡从集成分支 HEAD 切出；返工或续做从本卡分支 HEAD 接着做。
  - 执行器退出后，宿主自动 `git add -A` 并提交，执行器自己不需要提交。结果文件不纳入提交，靠 worktree 的 `info/exclude` 排除。
  - 合入后删除 worktree，分支保留到目标结束。
- **声明（claims）。**
  - 形式是 `{path, symbol?}`。Go 文件的 symbol 是顶层函数、方法（`Type.Method`）或类型；其他语言默认只到文件级。引入 tree-sitter 需要 cgo，v2.0 不做；以后要加须 使用者（owner） 同意。
  - 新建文件按确切路径声明。目录声明只表示“在此目录下新建文件”，不覆盖已有文件。
- **冲突规则只用于调度，不用于判对错。**
  - 同一 path 和 symbol 互斥。
  - 同文件不同 symbol 可以并行。
  - 文件级声明与同文件内的任何声明互斥。
  - 没有声明的卡可以直接运行，按乐观方式处理。
  - 冲突的卡进入 `waiting`，原因写明被哪张卡占了哪个符号。
- **读、测、复核不占声明。** 复核卡用只读 worktree，或者直接在卡分支上读。
- **收割时计算实际改动的符号。** Go 用 `go/parser` 比较前后差异。实际改动超出声明不算失败，只更新声明并记一条事件，冲突留给合并队列处理。
- **合并队列。** 每个目标一条，串行执行：
  1. 把卡分支三方合并到临时 worktree 的集成分支 HEAD 上。
  2. 跑卡的 `acceptance` 和项目的 `test_cmd`。
  3. 通过就快进集成分支。合并冲突就派一张变基卡，测试变红就派一张修复卡。两者都在原卡分支上接着做。
- **最大化并行由三处共同保证。** 隔离消除了目录锁；符号级声明让同一文件内的子功能能同时跑；拆卡卡按“骨架卡先行”的规则，让同一模组内互不干涉的部分也能并行（见第 6.4 节）。

## 5. 尝试结果与处置

收割顺序：读进程退出码；读事件流，尽力而为；看 diff；跑卡的 `acceptance`；宽松解析结果文件。最终 outcome 由工作区证据决定，事件流不完整不影响判定。

| outcome / 错误类 | 处置 |
| --- | --- |
| `passed`：有改动且验收通过；不产出代码的卡则以结果文件有效为准 | 开发类卡进入复核；设计、拆卡等卡按第 6 节吸收其产出 |
| `tests_failed` | 在本卡分支上返工，`rework_count+1`。超过上限（默认 2）就派一张待拍板问题 |
| `no_output`：没有改动，也没有有效结果 | 换一个候选渠道重派一次，仍然没有产出就派待拍板问题 |
| `result_unparsable` | 派一张 T1 整理卡提取结果，仍然失败就标为待复核（`needs_input`） |
| `quota_hit`：撞到额度，可能附带重置时间 | 渠道在重置前不可选。立即用下一个候选渠道新开尝试，从本卡分支已提交的部分接着做 |
| `auth_failed` | 渠道标为 `auth_failed`，推送一条通知；本卡换渠道重派。渠道恢复由 `cardex channel check` 或下一次成功调用触发 |
| `transport` / `timeout` | 同渠道退避重试，最多 2 次，之后换渠道。退避默认 30 秒，第二次加倍，可用 `loop.retry_backoff_seconds` 修改；到期的卡由调度每轮放回 `ready`，manual 目标也一样 |
| `launch_failed`：建 worktree、拼 prompt 或启动宿主时出错，执行器没有跑起来 | 清掉这次新建、还没有提交的 worktree 和卡分支；已经建了尝试的，尝试以此结束。卡按 `transport` 的退避规则等待，到期回到 `ready`，不换渠道。同一张卡连续 3 次启动失败，就派待拍板问题，附上出错的步骤和错误摘要 |
| `permission_denied` | 正常情况下应该被第 13 节的放行策略消除。出现时派待拍板问题，并附上被拒的操作 |
| `refusal` | 不自动换渠道，直接派待拍板问题 |
| `budget_limited`：单卡预算用尽 | 不算完成。目标预算还有余量时续做一次，否则派待拍板问题 |
| `violation`：触碰硬底线（第 13 节） | 尝试作废、分支保留供查，派待拍板问题 |

重试之所以安全，是因为每次尝试都在独立 worktree 里，不会 push，也没有外部副作用。路线在每次尝试派发前确定并记录；运行中不改道。

**留存（作者 2026-11-05 同意）。** 目的是事后能回答：一张卡为什么没完成，每一轮卡在哪，打回的理由是什么。

- 每次尝试的原始材料放在 `<home>/attempts/<id>/`：执行器输出 `output.log`、验收命令的完整输出 `acceptance.log`、收割时复制的结果文件 `result.md`。这些文件不随 worktree 删除，也不进事件，因为执行器输出里可能带凭据（第 13 节第 4 条）。
- 每次复核结论和每次返工都追加一条事件，写明卡、尝试、判定或返工来源（复核、测试失败、验收失败），以及意见全文。卡上的返工意见只是最新一条的便捷副本，历史以事件为准。
- 只保存，不归因。按原因汇总、报告和清理策略见第 17 节第 4 条。

## 6. 目标闭环

### 6.1 调度循环

`serve` 每隔几秒执行一轮，每一步都是幂等的：

1. 对账：收割已退出的宿主，处理孤儿进程。
2. 吸收产出：执行第 5 节的处置，以及第 6.3 节的卡产出规则。
3. 推进目标：只对 `mode=auto` 的目标执行第 6.2 至 6.5 节。
4. 推进合并队列。
5. 派发：从 `ready` 的卡里选，做声明冲突检查和路由选择（第 7 节），然后建 worktree（集成分支不存在时先创建，见第 3 节）、拼 prompt（第 8 节）、启动宿主。其中任何一步出错，都按第 5 节的 `launch_failed` 处置。
6. 余量填充（第 7.5 节）。
7. 到期的定时任务入队（第 10 节）。
8. 通知投递与提醒（第 9 节）。

**单轮出错（2026-11-06 定）。** 每一步单独兜底：一步出错，不跳过后面的步骤。派发按卡隔离，合并队列按目标隔离，一张卡或一个目标出错不挡其他卡。出错时写一条 `tick_error` 事件，记下步骤、卡或目标、错误摘要，不含执行器输出。同一步骤、同一对象、同一错误，10 分钟内只记一次。`serve` 不因某一轮出错而退出，只在启动时打不开数据库或配置无法解析时退出，并把原因写到 stderr。

`mode=manual` 的目标不会自动生成设计卡、拆卡卡或重规划卡，但派发、收割、复核和合并队列照常运行。这一点与 v1 的手动 workflow 能力等价。

### 6.2 设计树与分层冻结

- 新目标自动生成一个 `direction` 节点，并派一张设计卡。
- 设计卡的产出包括：更新节点文档、增删开放问题、提议子节点（`module` 或 `detail`），以及提议本节点“可冻结”。提议写在结果文件的 `## 可冻结`，整节只写“是”；还有前提或拿不准时不写，把前提列成开放问题。其他写法都不算提议，含“冻结”字样但不是这种短答的，交整理卡。
- 节点满足以下条件之一时进入 `freezable`：开放问题清空，或者只剩 使用者（owner） 类问题。
- 冻结确认按 `freeze_mode` 决定：

  | 层 | safe | fast |
  | --- | --- | --- |
  | direction | 使用者（owner） 确认 | 使用者（owner） 确认 |
  | module | 使用者（owner） 确认 | 自动 |
  | detail | 复核卡通过后自动 | 自动 |

  `freeze_mode` 可以随时修改，修改后只影响尚未冻结的节点。
- 节点冻结后立即为它派拆卡卡，兄弟节点和子节点继续设计。设计和开发就这样交错推进。
- 查一下就能知道的问题，不由设计卡自己查。设计卡把它列为 `fact` 问题，交给便宜模型的研究卡去查，再由下一张设计卡根据查到的事实下结论，见第 6.6 节。
- 原型卡（`spike`）用来回答“做了才知道”的问题。它在独立分支上实做，永不合并，结论回填到对应的开放问题。
- 重开：开发卡或复核卡可以在结果文件里报告“设计问题”。这会生成一条 `reopen` 问题。使用者（owner） 同意后节点变为 `reopened`：它下面尚未开始的卡变为 `paused`，正在跑的卡跑完再说，同时派一张重规划卡。

### 6.3 卡的种类

| kind | 默认 tier | 作用与产出 |
| --- | --- | --- |
| `design` | T3 | 推进一个设计节点 |
| `research` | T1 | 回答一批 `fact` 问题，并附证据，不下设计结论、不合并（第 6.6 节） |
| `spike` | T2 | 实做验证一个问题，不合并 |
| `plan` | T3 | 为已冻结的节点生成卡清单，并维护上下文包 |
| `skeleton` | T3 | 定接口和桩，供同模组的开发卡并行 |
| `dev` | 由分级决定 | 实现 |
| `review` | T3 | 复核开发卡，结论为 `pass` 或 `rework` 并附意见。必须由另一 family 的模型执行 |
| `repair` | T2 | 修测试变红，或按复核意见返工 |
| `rebase` | T2 | 解决合并冲突 |
| `replan` | T3 | 按实际结果调整节点下尚未开始的卡 |
| `normalize` | T1 | 从格式混乱的结果中提取结构 |
| `ranking` | T2，需要 `web` | 模型定位提案，见第 10 节 |
| `backlog` | T1 | 低风险积压项，供余量填充使用 |

### 6.4 拆卡、复核、重规划

- 拆卡卡在结果文件里输出 JSON 卡清单。每张卡的字段是 `key`、`kind`、`title`、`body`、`tier?`、`claims?`、`deps?`（引用 key）、`acceptance?`。
- 清单只做宽松校验：
  - 缺少的字段用默认值补上。
  - 无效的 deps 忽略，并记事件。
  - 依赖成环时断开一条边，并在节点上挂一个开放问题。
  - 超出本节点范围的卡转成设计问题，挂到对应节点。
- 骨架规则：如果几张开发卡的声明落在同一模组，同时引用了尚不存在的接口，拆卡卡应先列一张骨架卡，让这些开发卡依赖它。调度器不强制这一点，复核卡会检查。
- 开发卡 `passed` 后，自动派一张复核卡。复核卡的候选渠道必须满足“family 与作者不同”。结论 `rework` 时转为返工，`rework_count` 达到上限就派待拍板问题。
- 重规划：一个节点下由同一次拆卡产生的卡全部到达终态时，派一张重规划卡。节点被重开或触发熔断后也会派。它只能在本节点范围内增删或修改尚未开始的卡。

### 6.5 熔断、预算、完成

- **熔断**的单位是设计节点。以下任一条件触发时，暂停该节点并派 `breaker` 问题，其他节点照常推进：
  - 连续 3 次尝试失败；
  - 同一错误签名重复出现 2 次；
  - 同一张卡的修复卡连续 2 次未能让测试转绿；
  - 节点 6 小时没有合并成功，同时花费超过阈值。

  以上阈值都可以在配置里修改。
- **预算。** 目标的 `soft_tokens` 用到 80% 时发提醒，达到 `hard_tokens` 时暂停目标并派问题。单卡默认预算按 kind 设定。
- **完成。** 同时满足以下条件时，目标进入 `done_candidate`，并派 `done` 问题：所有节点都已冻结；没有未结束的卡；目标的 `acceptance` 在集成分支上通过。使用者（owner） 确认后，Cardex 把集成分支合入项目主分支。默认不自动合入主分支，也绝不 push。

### 6.6 事实调研：先问，再查，再定

设计、拆卡、重规划卡大部分的花费在“读”上。一个设计节点的推进拆成三步：

1. 强模型写问题；
2. 便宜模型并行查事实；
3. 强模型读事实、下结论。

这样省下的是强模型的读取量，结论仍由强模型给出。2026-10-30 追加，R1 不要求实现，落地分工见 plan.md。

- **盘点。** `mode=auto` 的新目标先派一张盘点用的 `research` 卡，再派 direction 设计卡。盘点内容包括：仓库结构、与目标标题相关的模块、现有文档、测试和构建命令。结果存为该目标的 `Q0`，之后每张设计卡都会带上它。
- **先问。** 设计、拆卡、重规划卡在 `## 开放问题` 里给每条问题加类型标签。
  - 标签有四种：`[fact]` 表示查一下就知道，`[experiment]` 表示要做了才知道，`[decision]` 表示需要权衡，`[owner]` 表示需要 使用者（owner） 决定。
  - 需要联网的事实问题写 `[fact web]`。
  - 没有标签的按 `decision` 处理。
  - 前言模板引导强模型这样做：附带的事实不够下结论时，不要自己大范围阅读，把缺的事实列成 `[fact]` 问题后结束本卡；定点核对几处文件可以自己做。这一条是引导，不是关卡，效果由成绩单按 kind 统计花费来观察。
- **再查。**
  - 吸收产出时，Cardex 把节点上 `open` 状态的 `fact` 问题按节点分组，每张 `research` 卡最多 5 个问题。`web` 问题单独成卡并带 `requires: web`。
  - 这些研究卡并行派发，tier 默认 T1。问题状态改为 `researching`，节点在这期间不派新的设计卡，其他节点照常推进。
  - 研究卡在 `.cardex-result.md` 的 `## 事实` 小节下，按 `### Q<id>` 分块作答。每块写：结论、证据（`文件:行号`、命令及输出片段，或 URL）、把握（高、中、低）、未能核实的部分。解析时宽松处理：缺证据的把握记为低；没有作答的问题记为 `unanswered`。
  - 研究卡与 `spike` 一样永不合并，产出只进库。`commit` 记派发时集成分支的 HEAD，`cited_paths` 从证据里的路径提取。
- **再定。**
  - 节点的研究卡全部到达终态后，派下一张设计卡（拆卡、重规划卡同理）。
  - 它的 prompt 带上本节点的事实摘要，`unanswered` 的问题注明“未能核实”。
  - 派发前检查每条事实的 `cited_paths` 在 `commit..HEAD` 之间是否有改动，有就标为 `stale`，在摘要中注明“可能过期”。Cardex 不会自动重查。
- **复查。** 设计卡对某条事实存疑时，在开放问题里写 `[fact] Q<id> 复查：<原因>`。Cardex 会以高一级的 tier 重查，`asks` 加 1。重查两次之后，这条问题转为 `decision`，由设计卡自己核实。
- **复用。** 拆卡卡把与开发有关的事实压缩写进上下文包 `context.md`，开发卡因此也少读一些。看板和 CLI 展示事实不在本次范围内。
- **不做。** 不要求每张设计卡之前都先调研。设计卡没提 `fact` 问题，就不派研究卡。也不依赖执行器自带的子 Agent，因为那样 Cardex 看不到它的额度和路由。

## 7. 路由与额度

### 7.1 选择流程

选择只在派发前进行，每次尝试做一次：

1. **显式指定优先。** 卡或目标显式指定的渠道或模型直接使用，不过滤红线，只检查健康状态。
   - 指定的渠道不存在、健康状态不为 `ok`，或在已知重置时间前被封锁，就算不可用；余量低于红线不算不可用。
   - 不可用时，按 §5 `quota_hit` 一行处理：只在本次请求里放开指定，按下面第 2–5 步选下一个候选。选中后把路线写回卡，并记一条事件，写明原指定、新路线和原因。目标上的指定不改。
   - 没有候选时，卡转 `needs_input` 并提一个问题。同一原因不重复提问，不算失败，也不增加尝试次数。backlog 卡不做这一步。
2. **确定 tier。** 取顺序是卡上的 tier，然后入队分级结果，然后 kind 的默认值。
3. **候选。** 定位表中满足该 tier 并具备 `requires` 能力的模型及其所在渠道。复核卡额外要求 family 与作者不同。本 tier 没有可用候选时，只允许向上借用一级，不向下借用。
4. **过滤。** 去掉健康状态不为 `ok` 的、达到并发上限的、余量低于红线的，以及在已知重置时间前被封锁的渠道。
5. **排序。**
   - 先按定位表的 `preference` 分组。
   - 同组内，同一 tier、同一来源的候选按 Pace 分排序：`score = 剩余比例 / max(距重置小时数, 1)`。已用超过 90% 降一档，超过 98% 排除。
   - 余量未知的渠道排在已知且有余量的渠道之后，配置里 `unknown_quota: allow|exclude`，默认 allow。
6. **记录。** 选择理由写入 `attempt.route_reason`：候选列表、被过滤的原因和最终分数。`cardex route explain <card>` 和看板都展示这份理由。

### 7.2 余量从哪里来

事实核实的结论是：七个执行器在派发前都拿不到稳定的剩余额度和刷新时间。所以 v2 自己估算，并标注置信度：

- **配置的额度窗口。** 每个渠道可以配置多个窗口，例如 5 小时、每日、每周、每月。每个窗口写明重置规则（固定时刻或滚动），以及可选的 token 或请求上限。
- **本地计量。** 把各次尝试的 usage 累加到对应窗口。usage 未知时按时长估算，并降低置信度。
- **观测到的撞线。** `quota_hit` 带回的重置时间作为硬封锁，同时用它校正窗口。
- **可选探针。** 只为已证实能自报余量的 CLI 写探针，v2.0 不要求。

剩余比例的计算是 `1 - 已用/上限`。没有配置上限的窗口算“未知”。

### 7.3 红线

- 每个渠道一条 `redline`，表示保留的比例。剩余比例小于等于红线时，该渠道不参与自动选择，只能被显式指定。
- v1 的 Claude 5 小时保底改用这个机制表达。

### 7.4 入队分级

- 时机：卡第一次派发前，如果 tier 为空就分级，结果写回卡。卡的创建点很多，派发前是唯一的汇合处。拆卡卡已经给出 tier 的卡不再分级，这样可以少调用模型。
- 做法：用配置的 `classifier` 渠道（便宜模型）跑一个固定 prompt，单次调用，超时 30 秒。结果按卡正文的哈希缓存。
- 失败时（超时、解析失败或渠道不可用），退回规则：按 kind 的默认 tier，开发卡默认 T2。

### 7.5 余量填充

满足以下全部条件时，从 `backlog` 卡中取一张派给该渠道：

- 某个渠道的窗口距重置不到 N 小时（默认 2 小时）；
- 剩余比例高于红线加上一个余量（默认 20%）；
- 本轮没有其他 ready 的卡要用它。

backlog 卡的来源有两个：拆卡卡和复核卡标注的 `backlog` 项（补测试、补文档、小重构），以及 使用者（owner） 手工添加的卡。

## 8. Prompt 与上下文包

- **拼接顺序固定**，让前缀尽量逐字节相同，以命中各家的 prompt cache：
  1. 按卡 kind 区分的 Cardex 固定前言。它是内嵌模板，同一版本内不变。
  2. 目标的上下文包：`<design_dir>/<objective>/context.md`，由拆卡卡和重规划卡维护，两次重规划之间不变。
  3. 相关设计节点的文档。
  4. 设计、拆卡、重规划卡附上本节点的事实摘要，包括 `Q0` 盘点，见第 6.6 节。
  5. 卡正文、声明、验收命令，以及返工意见（如果有）。
- **结果文件约定**：worktree 根目录的 `.cardex-result.md`。可选的小节有：
  - `## 结论`
  - `## 开放问题`（可带类型标签，见第 6.6 节）
  - `## 事实`（研究卡使用，按 `### Q<id>` 分块）
  - `## 设计问题`
  - `## 可冻结`（设计卡只写“是”，整理卡写“是”“否”或“不确定”；整节只有这一个词，见第 6.2 节）
  - `## 新卡`（```json）
  - `## 复核结论`（`pass` 或 `rework`，并附意见）
  - `## backlog`

  缺少结果文件不判失败，按第 5 节从工作区推断结果。
- 前言模板只写怎么做和怎么交付，不写流程仪式。

## 9. 通知

- **记录 origin。** 创建目标或卡时，用 `--origin <kind>:<id>` 记录创建者会话，例如 `codex:<thread>` 或 `hermes:<profile>/<session>`；也可以从环境变量 `CARDEX_ORIGIN` 读取。cardex-dispatch skill 负责传入。
- **投递顺序**：先投递到 origin，再投递到配置的后备目的地，最后写入 `$CARDEX_HOME/inbox/`（总是写入）。后备目的地是 `config.json` 的 `notify.fallback`，格式与 origin 相同，可以不配。卡有自己的 origin 时用卡的，否则用目标的。
- **hermes 订阅**：hermes 适配器还需要订阅 id，用作 inbox 子目录名。订阅 id 只来自下面的接收端，不从渠道账号推断。找不到匹配的接收端时，跳过 hermes 这一跳并记一条事件，不算投递失败，后备和 inbox 照常投递。
- **接收端（2026-11-06 定，第 10 轮实现，切换不等它）**：谁想收通知，就由它自己或它的 Agent 登记一个接收端。仓库里不带任何真实接收端，文档和测试只用占位名。
  - 存在 `config.json` 的 `notify.receivers[]`。每条有：
    - `id`：使用者自取，全局唯一；hermes 用它作 inbox 子目录名。
    - `adapter`：默认支持 `codex`、`hermes`，也可以用 `lark`、`file`。
    - `target`：codex 是 thread id；hermes 是 `profile`，可加 `session`；lark 是会话；file 是路径。
    - `root`：只用于 hermes，指 inbox 根目录，默认 `$CARDEX_HOME`。
    - `scope`：`projects[]`、`objectives[]`、`dir_prefixes[]` 或 `all: true`。都为空时不主动订阅，只在解析 origin 时使用。
    - `events`：要接收的问题种类（第 3 节 Question 的 `kind`），默认全部。
    - `enabled`。
  - 投递目标：origin、scope 命中且已启用的全部接收端、后备、inbox，按地址去重。origin 是 `hermes:<profile>/<session>` 时，在已启用的 hermes 接收端里找：session 相同的优先，其次是 profile 唯一匹配的；找不到或不唯一，就按上一条跳过并记事件。
  - 确认：hermes 接收端在同目录写回 `<wake>.ack.json` 后，记为已送达。没有确认的，按现有提醒和退避重投。
  - 命令：`cardex notify add | list | remove | test`。`add --from-origin` 从 `CARDEX_ORIGIN` 推出 adapter 和 target，Agent 登记自己只需要一条命令；`test` 发一条测试消息，用来确认对方收得到。命令改配置时要保证原子写入，不丢掉其他键。
  - 迁移：`import-legacy` 把 v1 的 `manager_wake.subscriptions` 转成接收端。`provider` 转 adapter；`thread_id`、`profile` 转 target；`projects`、`dir_prefixes` 原样保留；`task_ids` 换成导入后的目标 id；`event_types` 按语义映射为问题种类，映射不到的列进导入报告；`enabled` 原样保留。
- **适配器**：`codex`（沿用 v1 的 codex queue 投递方式）、`hermes`（沿用 v1 的 ack 方式）、`lark`（可选，经 lark-cli）、`file`。
- **规则**：至少投递一次，按问题 id 去重。未回复就按退避间隔提醒。未回复永远不等于同意。
- **回答方式**：`cardex question answer <id> <option>`，或者由管理会话通过 skill 调用。

## 10. 定时任务、成绩单与模型定位

- **定时任务**：`config.schedules[]`，形式是 `{name, every|cron-lite, card_template}`。`serve` 在到期时入队，用于模型定位、日报等。
- **成绩单**：由 attempts 和 events 聚合，维度是“模型 × kind × tier”。指标包括：复核通过率、合入率、平均返工次数，以及每张合入卡花费的 token。`cardex scorecard` 和看板展示它。
- **模型定位任务**：内置一张 `ranking` 卡模板，默认每周一次。它的工作是：
  1. 读取权威榜单上与软件开发相关的排名，来源清单固定写在模板里；
  2. 结合成绩单和各渠道当前可用的模型；
  3. 输出 `positioning` 提案和差异，并标出新出现的模型。

  提案生成一条问题，使用者（owner） 或管理会话 approve 后，写入新版本的定位表。已冻结的尝试不受影响。

## 11. 看板（只读）

- 由 `serve` 内嵌提供，服务端渲染 HTML，加少量轮询 JS，不用前端工程。所有路由都是 GET。
- **默认视图只显示当前的工作**：活跃目标、它们的设计树（节点状态和开放问题数）、卡依赖图的进度、运行中的尝试及其声明、合并队列、待拍板问题。
- **渠道视图**：健康状态、各窗口的估算余量和置信度、重置时间、红线、并发占用。
- **卡详情**：尝试历史、路由理由、outcome 证据。
- **历史**：已完成或已取消的目标、从 v1 导入的卡，默认收起，可以搜索。
- held 和 paused 不计入“活跃”。

## 12. 命令界面

```
cardex serve
cardex project add|list|show|set
cardex objective add|list|show|set|pause|resume|cancel   # set: mode, freeze-mode, budget, priority
cardex node list|show|freeze|reopen
cardex card add|list|show|cancel|retry|pause|resume|revive
cardex question list|show|answer
cardex channel list|show|set|check                       # set: redline, windows, concurrency
cardex route explain <card>
cardex positioning show|proposals|approve|reject
cardex schedule list|run <name>
cardex scorecard
cardex import-legacy [--dry-run] <v1-home>
cardex doctor
cardex host <attempt-id>                                 # 内部使用
```

## 13. 硬底线（仅此几条会阻断）

1. **不写 worktree 以外的地方。** 宿主用 macOS sandbox-exec 包住执行器，可写范围只有：本次 worktree、该 CLI 自己的 home 和缓存、系统临时目录。
2. **不 push、不安装、不改启动项。**
   - worktree 配置一个拒绝一切的 pre-push hook，`pushurl` 设为无效地址。
   - 沙箱只允许写上一条列出的位置，因此装软件和改启动项都会被挡住。
   - 收割时如果发现违规，outcome 记为 `violation`。
3. **预算硬上限。**
4. **不把凭据写进 prompt、事件或 Git。** 渠道凭据只通过各 CLI 自己的登录状态或 Keychain 使用。
5. **合入集成分支前，测试必须通过。**
6. **路线在派发前确定并记录，运行中不改。**
7. **需要 使用者（owner） 确认的动作**：合入项目主分支、切换 runtime、Release，以及 `forbidden_paths` 中的路径。

在上述沙箱内，执行器以自动放行模式运行，不再逐次审批。其他一切都是软约束：缺了就补，乱了就整理，解析不了就标为待复核。新增任何阻断条件，都必须写明它防的是哪类真实失败。

## 14. 迁移与切换

- **`import-legacy` 只读 v1 home。** 映射规则：
  - v1 的 `queued`、`running` 卡：变为 v2 的 `ready`，放进每个项目一个的“v1 导入”目标里。该目标的 `mode=manual`，不会自动生成卡。
  - v1 的 `held` 卡：变为 `paused`，保留 `legacy` 原状态和原因。可以用 `card revive` 逐张恢复，看板默认收起。
  - 其余（`done`、`failed`、`canceled`）：作为只读历史。
  - v1 的 FrozenRoute 和 `runner_pref`：只作为路由提示保存，不强制。
  - 事件不逐条迁移，只保留指向原文件的路径。
- **切换步骤**（需要 使用者（owner） 授权，由主对话和原安装 owner 执行；2026-11-07 按“v2 接管原位置”改写，逐条命令见切换手册）：
  1. 等 v1 没有正在跑的尝试：没有 `.lock`，tick 作业不在运行，没有 pid 仍存活的 `bound` 尝试；
  2. 卸载 v1 的 tick、board、manager-wake 三个 launchd 项，plist 移到回退目录；
  3. 把 `~/.cardex` 整个改名为 `~/.cardex-v1`；
  4. 安装 v2 二进制到 `/opt/homebrew/bin/cardex`，v1 的 `cardex` 和 `cardex-board` 移到回退目录；
  5. `cardex --home ~/.cardex import-legacy ~/.cardex-v1`，再写 v2 的 `config.json`（显式写 `schedules`）并配置渠道；
  6. 注册 `com.cardex.serve`：`serve --addr 0.0.0.0:8788`，PATH 让渠道名解析到各 CLI 已验收的外置包装；
  7. 冒烟测试：`doctor`、看板、一个小的真实目标；
  8. 发布新版 cardex-dispatch skill。
- **回退**：卸载 `com.cardex.serve`；`~/.cardex` 改名为 `~/.cardex-v2-failed`，`~/.cardex-v1` 改回 `~/.cardex`；恢复 v1 的两个二进制和三个 plist。v1 的文件从头到尾都没有被改动。`~/.cardex-v1` 的删除另行授权。

## 15. 不做（v2.0）

- 网关与协议翻译。
- 改各 CLI 的 home。
- 返工时续用原会话。
- 脱敏和插件。
- tree-sitter。
- 合并批量化（merge train）。
- 自动 push 和 Release。

## 16. 两处设计取舍（作者 2026-10-30 已同意）

1. **重试策略。** 第 5 节允许在 `quota_hit`、`auth_failed`、`transport`、`no_output` 时新开尝试，或者换渠道续做。这取代了 v1 “必须证明额度耗尽且零活动才回退”的规则。理由是工作区隔离以后，重试没有副作用。`refusal` 仍然不自动换渠道。
2. **执行器权限。** 第 13 节让执行器在沙箱内自动放行，不再逐次审批，用来消除 28 张权限拒绝卡这类失败。

## 17. 切换后的下一批（v2.1，作者 2026-11-02 同意列入）

本节不属于 v2.0：v2.0 的 lane 不实现，复核也不得据此提出要求。切换完成后，它作为新目标交给 Cardex 自己推进。参考来源是卡兹克 2026-11-02 介绍其 Agent 自动化系统的文章。

1. **决定台账。**
   - 每个项目一份台账，存在 store 里，同时导出为项目内的一个 Markdown 文件。
   - 台账分两类：“使用者（owner） 定的事”和“定了不做”。每条写 使用者（owner） 的原话、日期、来源问题 id 和适用范围。
   - 入账方式：使用者（owner） 回答问题时自动入账，使用者（owner） 也可以直接添加。代决和代答不入账。
   - 设计、拆卡、重规划卡的上下文包（第 8 节）带上与本节点相关的台账条目。
   - 派问题之前，先对照台账。对得上的，直接引用该条作答，记为 `answered_by_ledger`，不再推给 使用者（owner）。对不上，或者只是部分相关的，照常提问，并附上最接近的条目。
   - 执行器或复核主张有授权时，只认台账条目。
2. **问题附推荐答案。**
   - 每个问题都必须附推荐选项、一句理由，以及选其他选项的后果。缺推荐项的问题，先派一张 T1 整理卡补齐，再投递。
   - 飞书适配器改发交互卡片，每个选项一个按钮。点按钮后，回调经 lark-cli 写回 `cardex question answer`，卡片随即改为显示结论。
   - 未回复仍然不等于同意。
3. **尝试关联会话。** 每次尝试都记下执行器的会话 id 和日志路径。在看板上点开任意一张卡，都能跳到当时的会话。
4. **失败复盘（作者 2026-11-05 同意列入）。** 建在第 5 节“留存”的原始材料上。
   - 失败与停滞清单：命令和看板页，列出未完成或卡住的卡、卡在哪一步、最近几轮的判定和理由。
   - 成绩单加渠道维度，并在看板显示。
   - 每周一张便宜模型的归因卡（第 10 节定时任务）：读本周的留存材料，把失败按原因归类，例如任务描述不清、验收命令有误、上下文不够、模型能力不够、环境问题、写域冲突，并给出改进建议，结果推送给 使用者（owner）。分类表由归因卡提出、使用者（owner） 确认，不预先写死。
   - 停滞提醒：卡在同一状态超过阈值，或者目标超过若干天没完成时通知。
   - `attempts/` 的保留策略，例如按天数或总大小清理，清理前先保留归因卡已用过的摘要。
