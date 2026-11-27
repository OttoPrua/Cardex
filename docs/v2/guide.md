# Cardex 使用说明

这份说明给 使用者（owner）。命令以 `cardex` 的 `usage` 为准。数据目录用 `--home` 或环境变量 `CARDEX_HOME` 指向你选的一个目录。

## 安装

> **给部署 Agent：** 如果发现已有 V1 数据目录，先读[从 V1 升级到 V2](upgrade-from-v1.md)。首次安装直接从下方开始。

在仓库里构建 `cardex` 二进制，放到 `PATH` 上。第一次使用先选定数据目录，再跑 `doctor`。`doctor` 会检查渠道、窗口和定位表是否能派发。

```
cardex --home /path/to/cardex-home doctor
```

`project add` 绑定仓库：`--repo` 是主 checkout，`--test-cmd` 是默认验收命令，`--design-dir` 是设计文档目录。

## 配置渠道

渠道用 `channel list`、`channel show` 和 `channel set` 维护。新建时带 `--id` 和 `--runner`。已经存在的渠道可以只改其中几项。

窗口用 `--window`，形式是 `name:rule[:tokens]`。`doctor` 接受的窗口名是 `5h`、`daily`、`weekly`、`monthly`。例如日窗口固定 100 个 token：

```
cardex channel set --id ch1 --runner grok --redline 0.25 --model lumen-model --window daily:fixed:100
```

红线是 `--redline`，取值从 0 到 1，含两端。剩余比例小于或等于红线时，自动路由不再选这条渠道；目标或卡上显式写的渠道不受这道过滤。`channel check` 看当前健康和窗口。`doctor` 会指出红线越界或窗口名不在上面四个之中。

定位表决定哪个模型能做哪个等级。`positioning show` 看当前表，`positioning proposals` 列出提案，`positioning approve <id>` 让一份提案成为当前表。`positioning reject <id>` 目前返回错误，不改当前表。派发之后用 `route explain <card>` 看这次选了谁、候选等级和被过滤的原因。

接入一个新的 CLI 渠道，或者检查已有渠道时，按 [channel-setup.md](channel-setup.md) 一步步做。

## 写库前的备份

在 CardeX 数据目录的 `config.json` 设置 `"backups_dir": "backups"`，为已有数据库启用命令级备份。路径可以是绝对路径，或相对于该数据目录。Windows 请在启用前手工建好备份目录，缺失时命令不会写库。修改项目、目标、卡、节点、问题或渠道，批准定位表，手动运行排期，应用 `setup --yes` 和正式导入 V1 数据前，命令用 SQLite 在线备份生成一个独立快照，包含已经提交到 WAL 的事务。新建空库时没有旧数据可备份；未设置 `backups_dir` 时沿用原行为。

每份快照用独立名称保存，不覆盖或删除历史备份。快照的只读 `PRAGMA integrity_check` 必须恰好返回一条 `ok`。之后，只有快照内容及其目录项都完成落盘，命令才继续写库；无法校验、独占发布或同步备份时，命令会拒绝写库。不另外依赖 `sqlite3` 可执行文件。迁移需要备份时，同一命令只保留迁移前的那一份。读命令、只写外部配置的命令和后台进程的日常状态更新不会为每次操作新建快照。

在线快照是当时已提交状态的副本，不等同于撤销单条命令。恢复前先停用写入，并核对快照之后是否还有应该保留的修改。

## 创建目标

`objective add` 至少要 `--project`。常用的还有 `--title`、`--mode`、`--freeze-mode`、`--origin`。`--mode` 默认 `auto`，`--freeze-mode` 默认 `safe`。标题空着时目标会暂停。

`auto` 会自己做仓库盘点、方向和模块设计、事实研究和拆卡。`manual` 不自动生成卡，你用 `card add` 放入的卡仍会派发和合并。

`safe` 下，方向和模块冻结都来问你；细节层先走复核。`fast` 下，模块和细节在设计写明可冻结后直接冻结；方向层仍然来问你。方向层不论 `safe` 还是 `fast` 都不会自己冻结。

```
cardex objective add --project p --title 账单 --mode auto --freeze-mode fast --origin codex:<thread>
```

`objective list` 和 `objective show` 看状态。上下文压缩后用 `cardex objective brief <id>` 看当前阶段、已冻结范围、待批准事项和最近记录的决定（各显示最多三条，保留总数）。升级之前未记录时间的决定不冒充最近决定，可用 `question list` 查阅。`objective pause`、`objective resume`、`objective cancel` 改目标状态。

项目和目标可通过 `add` 或 `set` 的可重复 `--hard-rule "规则"` 写入卡片提示词共用的文本规则。每次派发和续跑都先继承项目规则，再追加目标规则；相同文本只出现一次。更新时传入整组规则，`--hard-rule=` 清除当前层；不传该参数则保留现有设置。这是提示词约束，不替代沙箱或禁改路径。

## 批量导入卡

`card import` 向一个已有目标批量建卡。默认只预览，不建目录或数据库，不记读数，不迁移库。

```
cardex --json card import --objective obj --file cards.json
cardex --json card import --objective obj --file cards.json --apply
```

`cards.json` 支持 `version: 1`，用 `cardex card import --help` 查看示例。每张卡填写 `id`、`kind`、`title`，并在 `body` 和 `body_file` 之间二选一。正文文件必须是清单所在目录里的 UTF-8 普通文件，路径按清单目录计算。另可指定 `deps`、`claims` 和 `acceptance`。一批最多 256 张；清单上限 2 MiB、单张正文 512 KiB、整批正文 8 MiB。目标、卡和依赖 ID 上限为 128 UTF-8 字节；每卡最多 256 条唯一依赖，可达图最多 16,384 条唯一依赖边。同一张卡内重复依赖会移除，保留首次出现的顺序。不认识或重复的字段报错。

预览检查目标、重复的卡 ID、直接或间接依赖是否存在及是否成环。若冲突很多，只显示前几项。如果 SQLite 有 WAL、SHM 或回滚日志，预览明确返回不可用，不会自行清理这些文件；请使用已安全检查点的同版本数据目录重试。预览不锁定数据。`--apply` 在现有数据库里重新校验，整批同时成功或回滚，不覆盖已有卡；有活跃 WAL 时仍以写入事务读到的最新数据为准。

导入命令不会执行正文或验收命令。成功导入的卡状态为 `ready`，运行中的调度器随后可能按正常流程派发，所以只对可信清单使用 `--apply`。受管执行器可以预览，不能用这个命令写卡。

## 设计时的事实研究

设计结果里的开放问题用标签区分。`[fact]` 是查一下就知道的，Cardex 派一张研究卡去查，不来问你。研究卡默认走便宜的 T1。`[experiment]` 做原型。`[decision]` 由设计卡自己权衡：问题留在设计节点上，不因此生成 使用者（owner） 的待办。`[owner]` 需要 使用者（owner） 决定。

研究卡写回 `## 事实`，按 `### Q<id>` 给结论、证据和把握。证据要能指向文件和行号。下一张设计卡的正文里会带 `## 事实摘要`。

怎样要求复查：在开放问题里写 `[fact] Q<id> 复查：` 加上原因。第一次复查会再派一张研究卡，等级从 T1 升到 T2。再写一次复查后，这题变成 decision，由设计卡自己核实，不再派研究卡。

## 回答问题

读选项用 `cardex --json question show <id>`。它给出 text、options 和 answer。普通 `question show` 只有 id 和答案，读不到选项。`question list` 包含已经回答的问题，不会自动排除。`question answer <id> <option>` 把一个已经列在该问题上的选项存下来。冻结题的选项是 `同意` 和 `拒绝`，第一个是同意。空答案不会被收下。

方向冻结、预算用尽、熔断和目标完成，都交给 使用者（owner）。管理会话只在 使用者（owner） 已经选好选项时替他提交。

使用者已批准重试时，`cardex question answer <id> approve --retry` 可在同一次写入中保存答案并将开发卡送回调度队列。它只接受调度器针对当前失败尝试发出的、明确绑定到这张卡的无输出或临时渠道故障问题，不用于预算、权限、登录、评审或设计决策。条件不满足时答案和卡都不变；旧数据没有调度器重试记录时也不自动重试。如只需记录答案，仍用不带 `--retry` 的命令；如果选项文字本身是 `--retry`，使用 `question answer -- <id> --retry`。重试由下一轮调度处理，不会在答题时启动执行器。

## 配置通知

管理会话先给自己登记一个接收者，再发一条测试。id 自己取。在这个会话里执行：

```
cardex notify add --id <自取 id> --from-origin
cardex notify test <id>
```

`--from-origin` 使用 `--origin`，没有该旗标时使用 `CARDEX_ORIGIN`。`codex:<thread>` 记成 codex 接收者，`hermes:<profile>/<session>` 记成 hermes 接收者。

Hermes 接收者读取 `<root>/control/manager-wake/hermes-inbox/<id>/*.json`。入站字段是 `schema`、`subscription_id`、`provider`、`session_type`、`session_id`、`profile`、`wake_event_ids`、`message`、`inbound_digest`、`ts`。读完后在同一目录写 `<wake>.ack.json`。确认字段是 `schema`、`wake_event_ids`、`inbound_digest`、`received_at`。`notify test` 要等这个确认文件出现才报告已经收到。

`config.json` 里一条完整的 `notify` 配置如下。`scope` 全空时这条只用来解析 origin，不主动订阅。`events` 为空表示全部问题种类。`root` 省略时 Hermes 收件目录落在本次数据目录。

```json
{
  "notify": {
    "fallback": "codex:thread-0001",
    "receivers": [
      {
        "id": "example-manager",
        "adapter": "hermes",
        "target": "profile-a/example-manager",
        "root": "/path/to/hermes-root",
        "scope": {
          "projects": ["p"],
          "objectives": ["obj"],
          "dir_prefixes": ["/path/to/repo"],
          "all": false
        },
        "events": ["freeze", "reopen", "breaker", "budget", "refusal", "done", "custom"],
        "enabled": true
      }
    ]
  }
}
```

`cardex notify list` 列出每个接收者以及 enabled 或 disabled。`cardex notify remove <id>` 删除一条。重复的 id 不会覆盖已有条目。

## 看板

`serve` 同时跑调度循环和只读看板。它在标准输出打印实际监听地址，用浏览器打开那个地址即可。不要自行指定被拒绝的保留端口。看板只展示，不改卡、不回答问题。

## 常见问题

- `doctor` 说某个等级没有健康渠道：给渠道补模型，并用 `positioning approve` 把该等级写进定位表。
- 目标一直暂停：`objective show` 看是不是缺标题、模式写错，或冻结模式不是 `safe`/`fast`。
- 卡停在 `needs_input`：`question list` 里有一张挂在这张卡上的问题。选好选项后用 `question answer` 提交。
- 想知道为什么没派到某条渠道：`route explain <card>` 看红线、定位和模型族过滤。
- 复核必须换模型族。作者族和复核族相同是路由结果，不是看板显示错误。

### 恢复自动换渠道后的卡

对当前这次失败，调度器必须先发出明确的重试选择。直接用 `cardex question answer --retry <id> approve` 回答并入队，或在选择已批准后显式执行 `cardex card recover <id>`。恢复优先使用原来明确指定的卡渠道；若没有卡渠道，则使用目标渠道和型号。目标不能使用时不换其他渠道；旧的批准也不能替新的失败解锁。这个限制不改变正常的自动换渠道。

修复和变基卡的原始诊断保留在本地；发送给执行器的卡正文只有错误类别及计数。可另外用 `cardex card recover --note <code> <id>` 附加一个固定提示：`boundary-check` 检查边界、`rerun-checks` 重跑验收、`resolve-conflicts` 解决冲突。该命令不接受自由文本，不会修改状态、渠道或等待原因。较旧、没有结构化重试记录的失败不会被自动重试；暂停后也不能绕过原有阻塞。
