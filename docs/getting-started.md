# 从管理会话开始使用 Cardex

推荐把 **Codex 或 Hermes 的一个独立会话作为管理 Agent**，在该会话能执行本地命令的机器上安装 Cardex。管理 Agent 负责理解需求、选择预设、创建任务和跟进结果；Cardex 负责本地队列与调度；Claude、Codex、Grok 等 CLI 负责执行。

这三层可以来自不同产品。例如，在 Codex 会话里管理任务，由 Cardex 调用已登录的 Grok CLI；这不会把 Grok 订阅变成 Codex 原生模型，也不需要把订阅密钥复制到管理会话。

## 1. 让部署 Agent 安装，并在同一对话继续引导

把下面这段交给你当前的 Codex / Hermes Agent；它执行下方对应系统的安装命令后，应继续问你下一步，无需你再发起一次配置请求：

```text
请按 https://github.com/OttoPrua/Cardex/blob/main/docs/getting-started.md 安装并配置 Cardex。
安装完成后直接读取安装器给出的 cardex-dispatch/SKILL.md，在当前对话继续引导我，
不要只回复安装成功。先告诉我检测到了哪些 CLI，再逐步确认我已有且希望使用的订阅，
协助完成所选服务的配置和登录。基于实际可用模型给出任务路由推荐表，
询问我采用推荐、调整部分还是暂不启用；我确认后保存。后续任务沿用，不要每次重问。
```

在管理会话中让 Agent 执行与你的系统匹配的一行命令。默认装入当前用户目录，不需要管理员权限。macOS / Linux：

```sh
curl -fsSL https://github.com/OttoPrua/Cardex/releases/latest/download/install.sh -o /tmp/cardex-install.sh && sh /tmp/cardex-install.sh --manager codex
```

Hermes 管理会话把 `--manager codex` 换成 `--manager hermes`。Windows PowerShell：

```powershell
& ([scriptblock]::Create((Invoke-RestMethod -ErrorAction Stop https://github.com/OttoPrua/Cardex/releases/latest/download/install.ps1))) -Manager codex
```

Hermes 使用 `-Manager hermes`。安装器校验下载包的 SHA-256，安装二进制和 `cardex-dispatch` skill，再输出本机 CLI 盘点和给当前部署 Agent 的接续指令；**不改现有 Cardex 配置、CLI 登录或 shell 配置，不启动任务**。已有二进制和变更过的 skill 会保留备份。企业环境若限制脚本执行，按本机政策下载 Release 包手动安装即可，无需放宽全局执行策略。

| 文件 | macOS / Linux | Windows |
|---|---|---|
| Cardex | `~/.local/bin/cardex` | `%LOCALAPPDATA%\Cardex\bin\cardex.exe` |
| Codex skill | `~/.agents/skills/cardex-dispatch` | `%USERPROFILE%\.agents\skills\cardex-dispatch` |
| Hermes skill | `${HERMES_HOME:-~/.hermes}/skills/cardex-dispatch` | `%HERMES_HOME%\skills\cardex-dispatch`，未设置时用 `%USERPROFILE%\.hermes` |

自定义 skill 根目录用 `--skill-dir PATH` / `-SkillDir PATH`。此处 Codex 路径依据[官方 Skills 文档](https://learn.chatgpt.com/docs/build-skills)，Hermes 依据[官方 Skills 指南](https://hermes-agent.nousresearch.com/docs/guides/work-with-skills)。若当前会话尚未自动发现新 skill，部署 Agent 应直接读取安装器输出的绝对 `SKILL.md` 路径，继续当前对话，不要求你重开会话。

安装器会打印绝对路径。可直接用该路径，或为当前终端添加 PATH：

```sh
export PATH="$HOME/.local/bin:$PATH"
```

```powershell
$env:PATH = "$env:LOCALAPPDATA\Cardex\bin;$env:PATH"
```

安装器是命令行程序，不能自己生成 Agent 对话；它会在成功输出的末尾给出 `CARDEX_AGENT_ONBOARDING` 接续提示、skill 绝对路径和下一步该问的内容。执行安装的 Agent 读取这些提示后继续沟通。**在普通终端里单独运行安装脚本，只会打印提示；需要由部署 Agent 承接对话。**

### 部署 Agent 应怎样问

| 阶段 | Agent 主动完成 | 给 owner 的问题示例 |
|---|---|---|
| 安装结束 | 说明当前管理会话、机器、数据目录和检测到的 CLI | “检测到了 Codex 和 Grok，但还没确认订阅。你已有并希望接入哪些订阅？不知道套餐也可以说产品名。” |
| 订阅配置 | 核实所选 CLI、登录状态和模型目录；逐个补齐配置 | “这个服务需要你在官方登录页面完成登录，完成后告诉我，我继续检查。” |
| 路由推荐 | 按实际可用模型展示任务等级、模型、思考档、复核方式与理由 | “按你的订阅，我推荐上表规则。采用推荐、调整部分，还是暂不启用？” |
| 确认保存 | 合并预设、读回并做无模型 dry-run | “规则已保存。后续任务将沿用这些预设。” |

每次只问当前缺少的信息；已有答案和手调配置直接复用。某个服务未验证通过时，明确列为待完成，并询问是否先用已验证的服务继续。**首次推荐等你确认后再保存，沉默不视为同意**；如果你之前已明确授权“直接采用推荐”，则展示后按该授权执行，无需再问。确认路由不会自动启动整条队列或消耗额度做模型测试。登录被打断时留在同一对话等你完成，接着做剩余步骤。

不使用安装脚本时，也可从 [Release](https://github.com/OttoPrua/Cardex/releases/latest) 下载系统对应包，解压后直接运行其中的 `cardex` / `cardex.exe`，并把 `skills/cardex-dispatch` 复制到上表的 skill 根目录。包内附带上手与配置文档。

## 2. 接入已有订阅，保存一份可调整的推荐

管理 Agent 先运行只读盘点：

```sh
cardex setup -inventory
```

它只识别 PATH 中的执行 CLI，认证状态统一标为 `not_checked`。CLI 存在不代表已登录、订阅有效或能使用某个模型。管理 Agent 应结合你已说明的订阅，查各 CLI 的官方登录状态和模型目录；只询问缺失信息，不读取或打印 token、cookie、API key。缺少某个 CLI 时，只安装你选用的执行器。

推荐表应包含 **任务等级、已可用的 runner / 精确模型 ID、effort、是否额外复核、理由**。例如：

| 等级 | 选择依据 | 默认复核方式 |
|---|---|---|
| `routine`：说明、整理、简单修复 | 已有订阅里足够完成任务、成本低且响应快的模型；使用其支持的较低思考档 | 作者检查和相关实跑 |
| `development`：常规功能与多文件修改 | coding 表现、仓库适配、稳定性与额度平衡；通常中高思考档 | 按具体风险决定 |
| `complex`：架构、疑难缺陷、复杂推理 | 已有且可用的强 coding 模型；使用其实际支持的高思考档 | 有具体风险或用户要求时复核 |
| `management`：拆分、状态跟进 | 能可靠理解合同和工具输出的较轻模型 | 简单管理无需自动加审核卡 |

管理 Agent 查阅 [Artificial Analysis](https://artificialanalysis.ai/) 的当前 coding 评测，如 [Terminal-Bench](https://artificialanalysis.ai/zh/evaluations/terminalbench-4-0)，记录日期和来源。排名是参考，还要比较评测使用的推理档、Agent 框架、价格、延迟、限额，以及账号实际可用模型。不要将评测名直接当成 CLI 模型 ID，也不要把榜首自动推荐给全部任务。网页不可访问时说明推荐依据和不确定性，不声称“最新排名”。

**这一步由管理 Agent 联网研究，Cardex 不会自动抓排名或悄悄更新模型。** 展示具体推荐后，Agent 主动询问是否采用，得到选择后再保存；已有明确采用授权时不重复问。你可以手调任何一项。以后按保存值执行，只有你要求刷新、订阅变化或模型不可用时再调整。已有管理或 owner 路由约束要保留；不可为导入预设覆盖它们。

首次使用 Claude 或 Codex 时，最低配置入口是：

```sh
cardex setup -runner codex       # 另一种选择：-runner claude
cardex doctor
```

`setup` 不安装、不登录、不调用模型，已有配置会拒绝覆盖。Codex 初始模型取 CLI 默认，思考档为 `medium`；Claude 使用 `sonnet` / `opus` / `haiku` 别名。只有其他订阅时，可先 `cardex init`，再让管理 Agent 按[配置参考](config.md)仅补充对应 runner 配置；无需为用 Grok 或 Kimi 强行购买 Claude。纯非 Claude 的新配置可将 `claude_bin` 显式设为空，并始终使用对应 runner 的预设；不要因此删除已有共享队列的 Claude 配置。

管理 Agent 将确认后的建议写成 `presets.json`，再导入：

```sh
cardex presets -file presets.json
cardex presets
```

导入文件是“预设名 → 配置”的映射，**不是整个 config.json**。下面是只使用已配置 Codex 的最小示例；省略 `model` 表示采用当前配置/CLI 默认，并不固定某个排行榜模型：

```json
{
  "routine": {
    "runner": "codex",
    "effort": "medium",
    "review_after": false,
    "note": "最小起点：沿用当前模型。正式推荐由管理 Agent 填写实际模型 ID、核实日期和评测来源。"
  }
}
```

Cursor 的预设不填 `effort`（档位编码在模型 ID 中）；agy 预设也不填 `effort`，其推理设置使用 `config.antigravity.effort`。不要给所有 runner 硬套同一档位。

正式建议应写明核实过的 `model`，并在 `note` 中记录依据、来源日期和复核条件。`review_after=true` 会自动创建额外审核卡，使用 Cardex 现有审核路由，可能消耗另一份额度；并不自动保证另一模型或独立审核者。需要单独指定审核执行器时按[工作流](workflows.md)创建审核任务，不要同时重复开启自动审核。

导入会更新同名预设、保留其他预设和配置键，并打印原配置备份路径。只影响以后使用 `-preset` 创建的卡；不重写已入队任务。它验证配置结构和 runner 配置，不验证订阅、联网、模型目录或实际模型执行。

## 3. 派出第一张卡

进入一个现有项目目录。先展示解析结果，再创建和执行指定卡：

```sh
cardex add -preset routine -dry-run -title "认识项目" -dir . "阅读项目，说明入口、运行命令和一个可改进点；不要修改文件。"
cardex add -preset routine -title "认识项目" -dir . "阅读项目，说明入口、运行命令和一个可改进点；不要修改文件。"
cardex list
cardex run TASK_ID
cardex log TASK_ID
```

将 `TASK_ID` 换成 `add` 返回的实际 ID。参数应放在 prompt 或任务 ID 前，例如 `cardex run -root PATH TASK_ID`。`-dry-run` 不创建任务也不调用模型；`run TASK_ID` 才执行这张卡。已有后台调度时，入队卡可能自动启动，只想准备可用 `add -hold`。

普通 `cardex add` 自动走一张 `direct` 卡：诊断、改代码、检查、修复，加上作者自检。显式 owner 形状优先。这一张卡没有评分表、反复确认，也不设独立设计门。已经保存的 Goal 策略或 onboarding opt-in 不会挡住这次普通提交，卡面仍是 direct，不是原生 Goal。显式 `-work-mode staged` 或 `-work-mode goal` 不写卡，并指向 `cardex workflow`。入队只是队列准入；依赖、归属和容量仍单独决定能不能启动。`cardex workflow fanout` 把一张已完成的设计卡拆成各自独立的方向卡，此时不创建验收卡。`cardex workflow accept` 只在每个必要方向都完成后创建一张验收卡；方向完成本身不是整个目标验收。`cardex workflow accept -complete` 只有在这张卡的结论是绑定当前候选产物的可采纳 pass 时，才把目标标为已验收。暂停、失败、未知结论或重复事件不验收，也不再开第二张验收卡。`cardex workflow goal-round` 保留已完成的卡并开启下一轮；目标已经验收后停止。达到最大轮次只是上限，不是成功。`cardex workflow goal-direction` 与 `goal-launch` 分别准入或启动一个原生方向；启动不传 `-p`、`--single` 或 `--prompt-file`。`cardex workflow goal-sync -task <id>` 只核对此方向自己的会话、attempt 和 native goal id。省略 `-task` 仍是串行默认，只同步当前 writer。一张方向返回不会同步另一张，也不会创建验收卡。一个原生 Goal 会话完成不是整个目标验收。已创建但仍是 `design`、尚未绑定证明的 workflow，用 `cardex workflow design-bind <id> -design-receipt PATH` 补绑 Initial/LatestValid；也可以只给 `-design-task ID`。必须恰好一个当前已完成证明。已有 writer、方向、验收或执行合同不可替换。

仍由管理会话组织：放行 held 的 integration / live / cutover、指定独立审核人或额外额度、Release / 生产队列 / 安装 / 对外发消息、held 或 unknown 之后的第二写者或更换 provider、把候选应用到生产数据根。手动 Goal 启动不传 `-p`、`--single` 或 `--prompt-file`。

写能力 native Goal（`goal-run -manual` 与 `-hosted` 同一 argv 消费者）默认把 Grok `--sandbox workspace` 和 `--permission-mode auto` 交给已安装的 Grok CLI。`auto` 只保留权限提示，不会变成 bypass / always-approve，也不会关掉沙箱。只读 Goal/卡继续用现有只读沙箱配置，即使写沙箱 opt-in 已设置。

若工作区以外还需要明确的写根，由 Owner 自己在 Grok 自定义 profile 里授权，再把精确名称写进 Cardex：

1. 在项目 `.grok/sandbox.toml` 或 `~/.grok/sandbox.toml` 增加 `[profiles.NAME]`，`extends="workspace"`，`read_write` 只列字面目录。项目文件覆盖用户文件中的同名 profile。
2. 在 `grok_build.write_sandbox_profile` 填入同一个精确 `NAME`。空或省略仍启动 `--sandbox workspace`。`off`、bypass、unrestricted 以及路径式名称会被拒绝。
3. 单次 `goal-run` 也可以选择一个**已经存在且适用**的 profile，不改共享配置：`cardex workflow goal-run ID -manual|-hosted -sandbox NAME`。选择只作用于这次调用。未知、过宽、`off` / bypass / unrestricted / `devbox` / 只读、或不含覆盖 linked Git common-dir 的字面目录授权的名称，会在启动前拒绝。环境变量不能覆盖这个显式选择。

Cardex 只把 `--sandbox NAME` 转给 Grok；它不写 `sandbox.toml`、不推导父目录、不自动授予当前机器。未知自定义 profile 会在启动前拒绝，不会回落到 `off`。权限提示与内核/Seatbelt 写根是两层；配置 opt-in 本身不是当前主机已获授权或生产已恢复的证明。

`budget_limited` 不是已完成、也不是自动接受。托管 Goal 在原生 `budget_limited` / Idle 上可以确认 stop，再走真实的 Wait、输出排空和 custody 回收；只向 PTY 写入 `/goal clear` 或 `/quit` 不算释放。回收 custody 之后，由管理者用 `design-result -decision successor` 消费精确的 task / session / attempt / revision、`budget_limited` 观察、真实结果产物和已核验输入身份，并遵守轮次与 custody 限制；这不是自动接受源结果。

托管 Goal 的 pause / resume / stop 在原生 permission prompt 仍待处理时不能向 PTY 注入 Esc、斜杠命令或回车：回车会选中提示的当前项（通常是允许）。此时返回 `control_unsupported` 或 `pause_not_confirmed`，请用正式 `cardex cancel` 结束该 producer。没有原子的“停掉当前 modal tool”的原生 API；未确认的控制不是 pause / complete / released。

单次调整不会改动保存的预设：

```sh
cardex add -preset routine -effort high -review-after=false -dry-run -dir . "调查当前测试失败的原因，不修改文件。"
```

这是 Codex 预设示例；Grok 使用 `-grok-effort`，各执行器模型参数也不同。更换 runner 时应选择另一预设，不能用 `-runner` 覆盖不同 runner 的预设。多步骤 Codex 任务必须用 `-fresh`，或拆成明确依赖的单步卡。

跑通后可以 `cardex board` 查看 <http://127.0.0.1:8787>，使用 `cardex daemon` 前台持续调度；macOS 也可 `cardex install-launchd`。不要把后台服务“已启动”当成任务“已完成”。

## 数据目录与常见问题

默认数据目录为用户主目录下 `.cardex`。管理 Agent 应记住它使用的机器、二进制路径和数据目录；Hermes profile、Codex 会话或远端机器不一定共享同一个 HOME。`CARDEX_ROOT` 或每条命令的 `-root PATH` 可指定独立数据根。`setup -root` 不会替后续命令永久记住这个目录。

| 现象 | 处理 |
|---|---|
| 找不到 `cardex` | 使用安装器打印的绝对路径，或添加当前终端 PATH |
| `setup` 提示配置已存在 | 保留当前配置；先看 `presets` 和 `doctor`，只改需要的键 |
| `doctor` 找不到 CLI | 在同一机器安装所选 CLI，或用 `setup -bin PATH` 指定新配置的路径 |
| dry-run 成功，但真实任务报认证/模型错误 | 用该 CLI 核实登录和账号模型目录，再更新预设；dry-run 不做真实请求 |
| 任务是 held | 看任务日志和原因，修复问题后再恢复；不要盲目新增同目标 writer |
| 预设与已有强制路由冲突 | 保留原约束，调整推荐；预设不能绕过准入或 owner 路由 |

## Windows 使用边界

Release 提供 `windows_amd64.zip` 和 `windows_arm64.zip` 对应架构产物（文件名包含版本号），内含 `cardex.exe`、指南与 skill。原生产物存在不等于每个 Windows 功能或真实订阅都已验收；原生测试范围及限制见 [Windows 说明](windows.md)。

Windows 不支持 hosted PTY Goal，也不启用需要严格进程退出证明的自动跨引擎切换。前台 `daemon` 可用于调度；`install-launchd` 仅适用于 macOS。高级 Bash / SSH / rsync 示例需要相应工具；`-verify` 的 shell 命令应先核实本机支持。使用同一环境的路径：Windows CLI 用 Windows 路径，WSL CLI 在 WSL 内安装和运行。

## 已有安装与旧名称

升级前保留当前数据目录和正在执行的任务；新手配置不负责迁移现有策略。安装器保留旧二进制备份，但不会停止运行中的任务或更新后台服务的路径。Windows 二进制被占用时，请先正常停止对应进程再重试。

原命令名是 `claudego`。源码安装可按需用 `make install install-shim` 保留兼容软链；旧数据目录迁移先查看 `cardex migrate -dry-run` 和现有配置，不要直接删除旧根。
