# Cardex 使用说明

这份说明给 使用者（owner）。命令以 `cardex` 的 `usage` 为准。数据目录用 `--home` 或环境变量 `CARDEX_HOME` 指向你选的一个目录。

## 安装

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

## 创建目标

`objective add` 至少要 `--project`。常用的还有 `--title`、`--mode`、`--freeze-mode`、`--origin`。`--mode` 默认 `auto`，`--freeze-mode` 默认 `safe`。标题空着时目标会暂停。

`auto` 会自己做仓库盘点、方向和模块设计、事实研究和拆卡。`manual` 不自动生成卡，你用 `card add` 放入的卡仍会派发和合并。

`safe` 下，方向和模块冻结都来问你；细节层先走复核。`fast` 下，模块和细节在设计写明可冻结后直接冻结；方向层仍然来问你。方向层不论 `safe` 还是 `fast` 都不会自己冻结。

```
cardex objective add --project p --title 账单 --mode auto --freeze-mode fast --origin codex:<thread>
```

`objective list` 和 `objective show` 看状态。`objective pause`、`objective resume`、`objective cancel` 改目标状态。

## 设计时的事实研究

设计结果里的开放问题用标签区分。`[fact]` 是查一下就知道的，Cardex 派一张研究卡去查，不来问你。研究卡默认走便宜的 T1。`[experiment]` 做原型。`[decision]` 由设计卡自己权衡：问题留在设计节点上，不因此生成 使用者（owner） 的待办。`[owner]` 需要 使用者（owner） 决定。

研究卡写回 `## 事实`，按 `### Q<id>` 给结论、证据和把握。证据要能指向文件和行号。下一张设计卡的正文里会带 `## 事实摘要`。

怎样要求复查：在开放问题里写 `[fact] Q<id> 复查：` 加上原因。第一次复查会再派一张研究卡，等级从 T1 升到 T2。再写一次复查后，这题变成 decision，由设计卡自己核实，不再派研究卡。

## 回答问题

读选项用 `cardex --json question show <id>`。它给出 text、options 和 answer。普通 `question show` 只有 id 和答案，读不到选项。`question list` 包含已经回答的问题，不会自动排除。`question answer <id> <option>` 把一个已经列在该问题上的选项存下来。冻结题的选项是 `同意` 和 `拒绝`，第一个是同意。空答案不会被收下。

方向冻结、预算用尽、熔断和目标完成，都交给 使用者（owner）。管理会话只在 使用者（owner） 已经选好选项时替他提交。

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
