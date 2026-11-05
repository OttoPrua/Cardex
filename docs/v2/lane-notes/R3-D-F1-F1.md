# R3-D-F1-F1

## 做了什么

提醒账本的空文件、坏 JSON、读失败和单条坏字段降为诊断。能解析的记录仍按原间隔去重，投递继续走 origin、后备目的地和 inbox。问题 id 一律带上原 id 的摘要，合法 id 不再原样当作 Hermes 任务、wake、ack 路径或 inbox 文件名。

## 没做什么，以及原因

未改 prompt、ranking、问题状态机、design.md、goals.md。未发真实消息。未宣称摘要抗碰撞。

## 接口请求

无。

## 已知问题

Hermes 仍只在同一次投递里核对一次 ack，不等待。

## 修复记录

可恢复的账本格式异常不能挡住另一次投递，也不能丢掉同文件里已解析的记录 → internal/notify/reminder.go:37 internal/notify/reminder.go:48 internal/notify/reminder.go:98 internal/notify/deliver.go:78 internal/notify/deliver.go:122 → TestReminderLedgerFormatFaultStillDelivers
不同问题的编码身份不能与另一原始 id 共用 Hermes 任务、首个 wake、ack 路径和 inbox 文件名 → internal/notify/hermes.go:99 internal/notify/hermes.go:149 internal/notify/deliver.go:227 → TestQuestionIdentityStaysDistinct
