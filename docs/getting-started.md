# 从管理会话开始使用 Cardex

推荐把 **Codex 或 Hermes 的一个独立会话作为管理 Agent**，在该会话能执行本地命令的机器上安装 Cardex。管理 Agent 负责理解需求、选择预设、创建任务和跟进结果；Cardex 负责本地队列与调度；Claude、Codex、Grok 等 CLI 负责执行。

这三层可以来自不同产品。例如，在 Codex 会话里管理任务，由 Cardex 调用已登录的 Grok CLI；这不会把 Grok 订阅变成 Codex 原生模型，也不需要把订阅密钥复制到管理会话。

## 1. 一个命令安装程序与配套 skill

在管理会话中让 Agent 执行与你的系统匹配的一行命令。默认装入当前用户目录，不需要管理员权限。macOS / Linux：

```sh
curl -fsSL https://github.com/OttoPrua/Cardex/releases/latest/download/install.sh -o /tmp/cardex-install.sh && sh /tmp/cardex-install.sh --manager codex
```

Hermes 管理会话把 `--manager codex` 换成 `--manager hermes`。Windows PowerShell：

```powershell
& ([scriptblock]::Create((Invoke-RestMethod -ErrorAction Stop https://github.com/OttoPrua/Cardex/releases/latest/download/install.ps1))) -Manager codex
```

Hermes 使用 `-Manager hermes`。安装器校验下载包的 SHA-256，安装二进制和 `cardex-dispatch` skill，再输出本机 CLI 盘点；**不改现有 Cardex 配置、CLI 登录或 shell 配置，不启动任务**。已有二进制和变更过的 skill 会保留备份。企业环境若限制脚本执行，按本机政策下载 Release 包手动安装即可，无需放宽全局执行策略。

| 文件 | macOS / Linux | Windows |
|---|---|---|
| Cardex | `~/.local/bin/cardex` | `%LOCALAPPDATA%\Cardex\bin\cardex.exe` |
| Codex skill | `~/.agents/skills/cardex-dispatch` | `%USERPROFILE%\.agents\skills\cardex-dispatch` |
| Hermes skill | `${HERMES_HOME:-~/.hermes}/skills/cardex-dispatch` | `%HERMES_HOME%\skills\cardex-dispatch`，未设置时用 `%USERPROFILE%\.hermes` |

自定义 skill 根目录用 `--skill-dir PATH` / `-SkillDir PATH`。此处 Codex 路径依据[官方 Skills 文档](https://learn.chatgpt.com/docs/build-skills)，Hermes 依据[官方 Skills 指南](https://hermes-agent.nousresearch.com/docs/guides/work-with-skills)。若当前会话尚未发现新 skill，让它直接读取安装后的 `SKILL.md`，或新开管理会话。

安装器会打印绝对路径。可直接用该路径，或为当前终端添加 PATH：

```sh
export PATH="$HOME/.local/bin:$PATH"
```

```powershell
$env:PATH = "$env:LOCALAPPDATA\Cardex\bin;$env:PATH"
```

随后在管理会话发送：

```text
使用 cardex-dispatch 帮我完成本机 Cardex 配置。
先确认本会话作为管理 Agent、Cardex 所在机器和数据目录，再盘点我已有的 CLI/订阅。
结合 Artificial Analysis 当前 coding 评测、CLI 实际模型目录和我的订阅权限，
推荐各任务等级的模型、思考档及是否复核，展示后按推荐保存；我提出调整时以调整为准。
后续任务沿用保存的预设，不要每次重新问；不要安装我没选的付费服务。
```

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

**这一步由管理 Agent 联网研究，Cardex 不会自动抓排名或悄悄更新模型。** 展示推荐后，在你已授权采用推荐的范围内直接保存；你可以手调任何一项。以后按保存值执行，只有你要求刷新、订阅变化或模型不可用时再调整。已有管理或 owner 路由约束要保留；不可为导入预设覆盖它们。

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
