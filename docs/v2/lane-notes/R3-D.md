# R3-D

## 做了什么

`prompt.Build` 按固定顺序拼接 kind 前言、`docs/design/<objective>/context.md`（`git show <revision>:<path>`，不读脏工作区）、节点文档、design/plan/replan 的事实摘要，以及卡正文、声明、验收命令和返工意见。同一 kind 的前两段逐字节一致；上下文包本身跨 kind 一致。事实摘要用 `model.OpenQuestion`，保留 `Q0`；`stale` 注明可能过期，`unanswered` 注明未能核实。十二种前言用 embed 内嵌。

`notify.OriginFromArgs` 解析 `--origin kind:id`，标志优先于 `CARDEX_ORIGIN`，id 是第一个冒号之后的全部内容。投递顺序是 origin、后备目的地、`$CARDEX_HOME/inbox/`。codex 只拼 `queue --thread --message`；hermes 写入 inbound，ack 的 schema 和摘要吻合才算成功；lark 只拼 `lark-cli`。外部命令走注入的 exec 和时钟。同一问题在更长的退避间隔过去之前不重复发送。

## 没做什么，以及原因

未做 ranking 模板（R4-C）。未做问题状态机，未把沉默当成同意（R3-A）。未发真实消息，未启动 provider CLI、launchd 或会话。未移植 v1 的 cursor、inflight 回执和 `delivery_uncertain`。

## 接口请求

`cmd/cardex` 还没有把 `--origin` / `CARDEX_ORIGIN` 交给 `notify.OriginFromArgs`，派发时也还没有调用 `prompt.Build`。最小改法：创建目标和卡时解析 origin，派发前用调用方给出的 revision 调 `Build`。本 lane 不改 `cmd/`。

## 已知问题

去重和提醒只保存在进程内，重启后同一问题会再次投递。hermes 只核对一次 ack，不等待。
