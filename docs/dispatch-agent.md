# 单独使用一个派发 Agent

在 **Codex 或 Hermes 中开一个管理会话**，让它加载 `cardex-dispatch`。它负责需求、任务范围、预设选择和跟进；Cardex 是本机调度工具，执行工作交给各 provider CLI。无需再创建一个 Cardex 执行卡来“管理 Cardex 自己”。

先按[新手指南](getting-started.md)一命令安装程序与 skill。Codex 默认 skill 路径是 `~/.agents/skills/cardex-dispatch/SKILL.md`；Hermes 是 `${HERMES_HOME:-~/.hermes}/skills/cardex-dispatch/SKILL.md`。也可以把仓内 [SKILL.md](../skills/cardex-dispatch/SKILL.md) 直接交给支持本地命令的管理 Agent 阅读。

## 可复制的角色提示词

```text
你是我的 Cardex 派发 Agent，当前 Codex / Hermes 会话负责管理，不直接承担普通的长篇代码实现。

先确认 Cardex 所在机器、二进制路径、数据根和项目目录，读取现有队列与预设，复用已有工作。
按 cardex-dispatch skill 盘点我已有的 CLI 和订阅，在已授权的 provider 范围内核实模型目录。
结合 Artificial Analysis 当前 coding 评测、实际订阅可用模型、成本和额度，为 routine / development /
complex / management 推荐 runner、模型、effort 和是否复核。展示理由后采用推荐；我手调的设置优先。
将预设持久化，记录来源日期。后续沿用，只有我要求或模型不可用时重新推荐。

收到任务后，确定目标、工作目录、写入范围和一个能检验结果的真实运行方式。
读队列确认没有重复任务或冲突 writer，再按保存的预设创建卡；明确依赖的工作才串联。
已经授权执行就完成派发并跟进，不要逐步骤问许可；只有缺少关键事实或超出授权效果时再问。
只准备草案时用 dry-run；只准备待运行卡时用 hold；执行指定 ID，避免误启动整个队列。
保留已有进度，修复实际失败。结束时告诉我完成了什么、怎么验证、还有什么没验证，并给任务 ID。

不要把订阅密钥复制进对话，不要新增未授权的数据接收方、购买服务或擅自上线/发消息。
```

这段角色提示词不会自动创建新会话、配置订阅或改变现有 Agent。将它放入你选定的管理会话即可。管理会话自己的模型由 Codex/Hermes 决定，Cardex 任务预设只决定执行卡的 runner/model，两者独立。

## 配置推荐的工作方式

1. `cardex setup -inventory` 发现已有 CLI；结合你已说明的订阅、官方登录状态与实际模型目录确认可用范围。订阅权益不明时只问缺少的一项。
2. 管理 Agent 阅读 [Artificial Analysis](https://artificialanalysis.ai/) 当前 coding 评测及适用的官方 CLI 文档，比较评测条件、effort、成本、延迟和额度。保留来源及查询日期，不以排行榜名代替实际模型 ID。
3. 展示简短推荐表；按已授权的默认推荐保存，你的手动调整优先。用 `cardex presets -file presets.json` 写入，不覆盖整个 `config.json`。
4. 后续 `cardex add -preset development ...` 直接沿用；卡面固化当次 runner/model/effort/复核选择。更新预设不改变旧卡。

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

遇到限额按 Cardex 的现有冷却策略等待。认证或结果不明时，先查日志、已有工作和进程状态，再修复；不要重复派同一个 writer。预设是方便复用的建议，不绕过工作流门、既有 owner 路由或平台限制。
