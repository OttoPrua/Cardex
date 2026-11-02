# R4-A-F1

## 做了什么

看板仍只读，`Handler()` 零参数。设计节点透传 parent 与 layer，HTML 按父节点嵌套。卡详情展示已有退出码、起止提交和 `attempt:<id>` 事件。历史页码在乘页大小前夹到真实页数，解析失败回第 1 页。最后一个 `--home` 或 `--home=` 覆盖先前的值和 `CARDEX_HOME`。无上限窗口显示未知，有上限且剩余 0 显示耗尽。含 `..` 子串的普通 id 可打开，路径越界仍 404。

## 没做什么，以及原因

未改 `cmd/cardex`（R4-B 写域）。未改 store、model、调度、loop、e2e。未合并、未 push。未处理 N1–N6 以外的问题。

## 接口请求

`Handler()` 仍无参数，看板自己扫 `os.Args`。嵌入方若不用进程参数挂看板，这里看不到 serve 已经选定的 home。不要为了传 home 去改 `cmd/cardex`，除非复核允许给 `Handler` 加参数。

## 已知问题

历史仍先读全表再分页。日志仍只返回末尾 32KiB。无上限窗口的 Remaining 仍留在库里，看板不再把它当成估算。

## 修复记录

设计节点保留已存储的父节点和层级，HTML 把子节点嵌进父节点 → internal/board/data.go:210, internal/board/data.go:216, internal/board/templates/page.html:125 → TestDesignTreeKeepsParentAndLayer
卡详情展示已有退出码、起止提交和 attempt 事件，未设置的退出码保持空 → internal/board/data.go:360, internal/board/data.go:377, internal/board/templates/page.html:98 → TestCardShowsOutcomeEvidence
历史页码在乘法前夹到真实页数，解析溢出和负数回到第 1 页 → internal/board/api.go:146, internal/board/api.go:210, internal/board/data.go:429 → TestHistoryPageClamp
看板 home 取最后一个 --home 或 --home=，并覆盖 CARDEX_HOME → internal/board/api.go:306 → TestHomeLastWins
未配置上限的额度窗口显示未知，有上限且剩余 0 显示耗尽 → internal/board/data.go:506, internal/board/templates/page.html:83 → TestQuotaUnknownVersusExhausted
数据库已接受且不含分隔符的 id 不被拒绝，越界 id 仍 404 → internal/board/api.go:57, internal/board/api.go:64, internal/board/api.go:71, internal/board/api.go:221 → TestSafeDotDotIDs
