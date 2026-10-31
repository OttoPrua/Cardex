# R4-A

## 做了什么

`board.Handler()` 仍是零参数。看板用进程 `--home`，否则用 `CARDEX_HOME`，打开 `{home}/cardex.db` 并复用连接。GET 提供 HTML 和 `/api` JSON：`/` 与 `/api/work`，`/channels` 与 `/api/channels`，`/cards/{id}` 与 `/api/cards/{id}`，`/history` 与 `/api/history`。其他方法 405。HTML 来自 embed 的 `html/template`，每页用原生 `fetch` 轮询。默认视图只含 `active` 目标、节点状态、非 `answered` 的开放问题数、这些目标上非 paused 且非 v1 导入卡的状态计数和 waiting 原因、未结束尝试及其声明、`merge_queued`，以及这些目标上 answer 为空的拍板问题。渠道视图含健康、窗口余量、置信度、重置时间、红线、未结束尝试数和并发上限。卡详情含尝试历史、`route_reason`、outcome、错误摘录，以及 `/attempts/{id}/output.log` 的末尾 32KiB。历史含 `done`/`canceled` 目标和 legacy 非空的卡，可用 `q`、`status`、`page`（每页 50）。S22 已取消 skip。

## 没做什么，以及原因

没有写操作和登录。没有改 `cmd/cardex`、store、model。没有调度、合并、通知、导入或成绩单。没有前端工程或第三方脚本。`done_candidate` 不进默认视图，也不进历史。

## 接口请求

无。列表和单条读取都用现有的 List 与 Get。

## 已知问题

legacy 的 status、source 或 reason 任一非空，卡就只出现在历史里，即使它挂在 active 目标上且状态已是 ready。历史页先读全表再分页。日志只返回末尾 32KiB。
