# 单独使用一个派发 Agent

在 **Codex 或 Hermes 中开一个管理会话**，让它加载 `cardex-dispatch`。它负责需求、任务范围、预设选择和跟进；Cardex 是本机调度工具，执行工作交给各 provider CLI。无需再创建一个 Cardex 执行卡来“管理 Cardex 自己”。

先按[新手指南](getting-started.md)一命令安装程序与 skill。Codex 默认 skill 路径是 `~/.agents/skills/cardex-dispatch/SKILL.md`；Hermes 是 `${HERMES_HOME:-~/.hermes}/skills/cardex-dispatch/SKILL.md`。也可以把仓内 [SKILL.md](../skills/cardex-dispatch/SKILL.md) 直接交给支持本地命令的管理 Agent 阅读。

## 可复制的角色提示词

```text
你是我的 Cardex 派发 Agent，当前 Codex / Hermes 会话负责管理，不直接承担普通的长篇代码实现。

如果你负责部署，安装后读取安装器给出的 skill，在同一对话继续询问订阅和路由选择，不停在安装成功。
先确认 Cardex 所在机器、二进制路径、数据根和项目目录，读取现有队列与预设，复用已有工作。
按 cardex-dispatch skill 盘点我已有的 CLI 和订阅，在已授权的 provider 范围内核实模型目录。
结合 Artificial Analysis 当前 coding 评测、实际订阅可用模型、成本和额度，为 routine / development /
complex / management 推荐 runner、模型、effort 和是否复核。展示具体表格和理由，询问是否采用，
我确认后再保存；已有明确采用授权时不重复问，我手调的设置优先。
将预设持久化，记录来源日期。后续沿用，只有我要求或模型不可用时重新推荐。

收到任务后，确定目标、工作目录、写入范围和一个能检验结果的真实运行方式。
读队列确认没有重复任务或冲突 writer，再按保存的预设创建卡；明确依赖的工作才串联。
已经授权执行就完成派发并跟进，不要逐步骤问许可；只有缺少关键事实或超出授权效果时再问。
只准备草案时用 dry-run；只准备待运行卡时用 hold；执行指定 ID，避免误启动整个队列。
保留已有进度，修复实际失败。结束时告诉我完成了什么、怎么验证、还有什么没验证，并给任务 ID。

不要把订阅密钥复制进对话，不要新增未授权的数据接收方、购买服务或擅自上线/发消息。
```

这段角色提示词不会自动创建新会话、配置订阅或改变现有 Agent。将它放入你选定的管理会话即可。管理会话自己的模型由 Codex/Hermes 决定，Cardex 任务预设只决定执行卡的 runner/model，两者独立。管理会话与 Perlica/Codex 外部管理用量不会自动记到 Cardex 执行卡上。执行卡 `list -json` / `workflow show` 的 `raw_usage` 只收录已观察到的 provider/native 字段：已知零与省略分开，未接入的字段保持 unavailable。

## 配置推荐的工作方式

1. `cardex setup -inventory` 发现已有 CLI；结合你已说明的订阅、官方登录状态与实际模型目录确认可用范围。订阅权益不明时只问缺少的一项。
2. 管理 Agent 阅读 [Artificial Analysis](https://artificialanalysis.ai/) 当前 coding 评测及适用的官方 CLI 文档，比较评测条件、effort、成本、延迟和额度。保留来源及查询日期，不以排行榜名代替实际模型 ID。
3. 展示具体推荐表，询问采用、调整还是暂不启用；得到选择后保存，已有明确采用授权时不重复问，你的手动调整优先。用 `cardex presets -file presets.json` 写入，不覆盖整个 `config.json`。
4. 后续 `cardex add -preset development ...` 直接沿用；卡面固化当次 runner/model/effort/复核选择。更新预设不改变旧卡。
5. 自动与管理会话的边界：一个可验证结果用普通 `add` 走一张 direct 卡（诊断、实现、检查、修复和作者自检）。显式 owner 形状优先，不设评分表、反复确认或独立设计门。已保存的 Goal 策略或 onboarding opt-in 不阻挡这次普通提交，卡面仍是 direct。显式 `-work-mode staged` / `goal` 不写卡，并指向 `cardex workflow`。入队不等于可启动。`cardex workflow fanout` 把已完成的设计拆成独立方向卡，不创建验收卡。`cardex workflow accept` 在每个必要方向完成后只创建一张验收卡；新卡 prompt 含 overall_goal、completion_criteria、design_result/digest 与成员产物。方向完成不是整个目标验收。输入标准变化后不改写在途或已通过的旧验收卡，也不把旧覆盖当作新标准。`cardex workflow accept -complete` 只在该卡结论是绑定当前候选产物的可采纳 pass 时验收。暂停、失败、未知结论或重复事件不验收，也不再开第二张验收卡。看板排队依赖显示等待谁/为何（running/held/failed/unknown/missing），前驱恢复后更新；协调卡 `{{QUEUE}}`/`{{PROGRESS}}` 按显式 project/workflow/depends_on 有界注入并披露省略。`cardex workflow goal-round` 保留已完成的卡并开启下一轮，目标已验收后停止；达到最大轮次只是上限，不是成功。`cardex workflow goal-direction` 与 `goal-launch` 准入或启动一个原生方向。`cardex workflow goal-sync -task <id>` 只核对此方向自己的会话、attempt 和 native goal id。省略 `-task` 仍是串行默认，只同步当前 writer。一张方向返回不会同步另一张，也不会创建验收卡。一个原生 Goal 会话完成不等于整个目标验收。管理会话仍负责：held 的 integration/live/cutover、独立审核人与额外额度、Release/生产队列/安装/对外消息、held 或 unknown 后的第二写者、把候选应用到生产数据根。手动 Goal 启动不传 `-p`、`--single` 或 `--prompt-file`。写能力 native Goal 默认 `--sandbox workspace` 且 `--permission-mode auto`（提示保留）。额外写根由 Owner 在 Grok `[profiles.NAME] extends="workspace" read_write=["/path/to/dir"]` 中自行授权，并把精确 `NAME` 写入 `grok_build.write_sandbox_profile`。Cardex 只转发 `--sandbox NAME`，不写 sandbox.toml，不自动授予当前主机。只读卡不受该 opt-in 影响；禁止 `off` / bypass。仅在临时假根上，`cardex workflow goal-bootstrap-before-native-recovery <id> -root ROOT -authorization FILE -manual|-hosted [-executor-capture FILE -executor-digest SHA256 -executor-call-id CALL]` 是一条独立的单次授权恢复入口；普通 `goal-run` 对 started/unknown 仍 fail-closed。准入要求经真实 launch 路径捕获的、绑定原 attempt 及其实际非零退出的精确 pre-provider launcher 拒绝输出，外加进程退出、custody 已回收、且无 native Session/Goal。Hosted PTY 是正式捕获消费路径：单一 master drain 把同一有界整行精确匹配器 tee 进去，child stderr 仍挂在 PTY slave。直接手动 controlling TTY stderr 保持原样，该次 launch 不产生此证明。无 TTY 的 headless stderr 仍走同一匹配器。stdin/stdout TTY 与 stderr 分开。宽泛 FailureClass 与从 native 缺失推断的 SemanticEvents/ModelEvents/ToolEvents=0 不是该证明。generic 成功/无输出、generic 失败、陈旧 FailureClass、仅 OS start+exit+native 缺失、仅目录缺失、缺失/不可读/畸形/矛盾证据、以及不可读/畸形 native 产物一律拒绝。owner 授权不是执行事实证明；缺少保留证据的记录保持阻断。原 `.bootstrap-refusal` sidecar 缺失时，同一入口增加最薄的 manager-admitted 选项：`-executor-capture FILE -executor-digest SHA256 -executor-call-id CALL`。FILE 必须是字节精确的 3 条 Codex executor JSONL（`custom_tool_call` + 子 `CommandExecution` + `custom_tool_call_output`）。CLI digest/call-id 必须与单次授权中的同一字段一致，授权还绑定 item id、return id 与 provenance。恢复会重验原始字节 SHA256、从 provenance 源选出的三条记录、精确 call/item/return 身份、call-input 与 CommandExecution 的精确 goal-run 命令、stdout/aggregated/return 通道各自的拒绝文本、对照原 attempt 的有序 JSONL 时间戳、终端非零退出、提取输出上现有精确 pre-provider 拒绝匹配器，以及 session/contract/profile 绑定。匹配后跳过 sidecar 要求，且绝不补写缺失的 `.bootstrap-refusal`。用户自造 JSON、自哈希新写 triple、摘要、LLM 文本、generic 错误、事后 approved retry、更晚的失败事件、截断/改写/重序列化字节、未绑定 digest/event、绑定不一致一律拒绝并指出缺失事实。授权文件校验显式单次 owner 决定，绑定 workflow/writer/revision/original attempt/session/contract digest/profile digest，以及 executor 证明下的 original raw digest、call/item/return 身份与 provenance，不授予业务许可；已消费或重放的授权被拒绝。测试只在假 fixture 上签发。样例：`{"kind":"bootstrap-before-native-recovery","workflow_id":"wf-fake","writer_task_id":"t-fake","revision":3,"original_attempt_id":"at-fake","session_id":"00000000-0000-4000-8000-000000000001","contract_digest":"sha256-of-frozen-stage-contract","profile_digest":"sha256-of-sandbox=workspace","issued_at":"2026-10-05T00:00:00Z","expires_at":"2026-10-05T01:00:00Z","nonce":"bn-fake","executor_raw_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","executor_call_id":"call_FAKE0001","executor_item_id":"exec-00000000-1111-2222-3333-444444444444","executor_return_id":"ctco_fake","executor_provenance":"/tmp/fake-retained-executor.jsonl"}`。live/cutover/A/B 与真实 unknown 的组织仍由 owner/manager 负责；本入口不是生产自动化，不写、不启动、不重试、不改生产 A/B 记录。新鲜 successor/revise 设计回执上的可选 `add_write_paths` 仅在 digest 核验过的 result 内声明了同一 JSON 数组时绑定（正文提及或子串不算声明）；扩域与 scheduler/admission 临界区只作用于该已核验节点真正扩域的 successor/revise，stop/input/accept 与无该字段的回执沿用原 per-workflow 控制路径；历史写者/合同不变；拒绝时不做部分扩域。缺省回执无该字段时行为不变。

新卡可用 `cardex add -verify 'go test ./...' -verify-on-success` 在 provider 成功收尾时跑验收（含无改动），并把实际结果记入既有 task/events；未加 `-verify-on-success` 的旧卡保持原收割验证。远程走已配置主机适配器的远端 cwd，否则显式拒绝。

通用模板可用 `cardex templates status -root ROOT` 查看来源/差异，用 `cardex templates refresh -root ROOT NAME` 显式刷新选定文件（先备份）。没有可信 shipped 基准时显示 UNKNOWN，不要把新版本 hash 当成用户未改过。

没有某一等级专用模型时，可以让多个预设复用同一可用模型。不要为填满表格新增订阅或强行做多模型审核。需要模型不可用时的替代方案，应在原 provider/隐私授权范围内明确选择，不把失败当成向新 provider 发送项目内容的授权。

## 案例一：一个常规修复

对管理会话说：

```text
在当前项目修复空搜索结果导致的页面崩溃。只改搜索结果处理和相关测试，
按 development 预设执行，跑项目现有相关测试，完成后给我改动与实跑结果。
```

管理 Agent 确认项目、现有卡和预设，创建一张有界任务。项目路径已由当前目录确定时：

```sh
cardex list -json
cardex presets
cardex add -preset development -dry-run -title "修复空搜索结果" -dir . "修复空搜索结果导致的页面崩溃。只改相关结果处理及测试，运行现有相关检查并说明结果，保留无关改动。"
cardex add -preset development -title "修复空搜索结果" -dir . "修复空搜索结果导致的页面崩溃。只改相关结果处理及测试，运行现有相关检查并说明结果，保留无关改动。"
cardex run TASK_ID
cardex log TASK_ID
```

`development` 必须先由推荐流程保存。小任务用作者检查和相关运行即可；预设不需要默认加独立审核卡。

## 案例二：先规划，再实现

```text
给项目添加 CSV 导入。先调查当前导入路径并给出方案；确认字段兼容和错误处理后，
在现有代码上实现并运行一个真实导入样例。外部服务和数据库生产数据不在本次范围。
```

管理 Agent 可以先派只读调查卡，再根据结果细化实现卡，避免在事实不明时冻结全部计划。如果两个步骤和输入已确定，可保存一个 `task.md`：

```markdown
阅读现有 CSV 导入相关代码，明确字段兼容和错误处理，记录需保留的现有行为。
---
按已确定方案实现 CSV 导入，运行真实导入样例与相关检查，报告结果。
```

```sh
cardex add -preset development -fresh -file task.md -title "CSV 导入" -dir . -dry-run
```

去掉 `-dry-run` 后入队。`-fresh` 使步骤使用新会话，因此每一步所需状态应保存在项目内的既有文件或明确产物中；不要假设上一步对话自动传递。Claude 可在适合的任务里省略 `-fresh` 保持会话连续；Codex 多步骤必须保留该参数。

## 案例三：高风险改动需要明确复核

```text
检查并修复支付回调重复记账问题，仅操作本地测试数据。
用 complex 预设，覆盖重复回调和并发回调；完成后做针对性对抗复核。
不要部署，不访问生产凭据。
```

此处额外复核的理由是资金/并发正确性。管理 Agent 应明确复核方法、执行者与验收结果；若已安排独立审核者，不再同时开 `review_after` 自动重复派审。自动审核使用现有审核路由，不能单凭“有审核卡”宣称模型多样性或独立审查。要用机器约束的集成门时，采用[推荐工作流](workflows.md)。

## 结果如何跟进

`run TASK_ID` 可以等待目标执行；长任务查看 `list -json`、`log TASK_ID` 和 `digest`。常规状态变化不用频繁打扰用户；有实质结果、阻塞或需要决策时简短反馈。`done` 是调度记录，还要读日志中的实际检查与产物，才能描述功能是否可用。

遇到限额按 Cardex 的现有冷却策略等待。认证或结果不明时，先查日志、已有工作和进程状态，再修复；不要重复派同一个 writer。失败卡用 `cardex retry ID` 重新入队并保留会话；`cardex retry -fresh ID` 仅在 retry 已合法且本卡 custody 已回收时开新会话（不 `--resume`），不绕过 held/集成门/sealed/no-retry/原生 Goal 身份，LastError 不是许可，不改 provider/model。预设是方便复用的建议，不绕过工作流门、既有 owner 路由或平台限制。

## 新任务的可配置路由

已有 Owner 路由配置可在同一个 `config.json` 中编辑 `work_class_routes` 与 `route_matrix`；普通安装继续沿用预设，无需额外开启 Owner 矩阵。配置片段、支持的 runner 和优先级见 [派发 skill](../skills/cardex-dispatch/SKILL.md#configurable-routing-for-new-work)。`route_matrix` 使用 high/medium/low（兼容 opus/sonnet/haiku）× frontend/backend/general，重复别名拒绝。用 `cardex route -root ROOT -complexity medium -category general` 预览；`add -model sonnet -route-class general` 和相同字段的 emitted task 使用同一解析器。显式 work-class 优先选择类别路由，显式执行器/模型/会话仍优先保留。

`fallback` 缺省/null 继承原默认，`[]` 禁止自动回退，非空数组严格保留声明的顺序、不追加默认 provider。配置仅作用于新准入任务，旧完整快照保留全部执行腿与审核要求；既有已证明零活动的跨模式额度续作仍保留原 attempt 证据、冻结新准入路线。daily 的 gpt-complex 默认是 gpt-6.1-sol/high（可显式 medium），gpt-short 是 Grok 4.6/xhigh；原有风险、只读、额度和不确定状态限制继续生效。

需要 Goal 写者继承配置时，`workflow init ... -writer-engine auto -reviewer-engine grok-build -model sonnet -route-class general`，随后 `workflow writer -root ROOT -mode manual WF_ID`。也可声明 `-work-class development`。显式引擎的旧 workflow 不改路由；原生方向仅在实际 Grok 能力允许时启动，各方向使用独立会话。方向结束后仍由管理 Agent 同步所有必要方向，并用已有 `workflow accept` 统一验收；程序不自动决定下一轮目标或 Release。验收按完成标准与实际产物进行，深度与风险相称；失败保留产物、修具体问题、只复验受影响部分。
