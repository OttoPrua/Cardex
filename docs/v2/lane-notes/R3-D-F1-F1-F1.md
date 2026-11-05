# R3-D-F1-F1-F1

## 做了什么

已有提醒账本读失败时不再当成空账本写回。另一次新问题仍按 origin、后备目的地、inbox 的顺序投递，返回的错误带上这次读失败，原文件字节保持不变。缺文件仍会新建。空文件、坏 JSON 和单条坏记录仍只作诊断，并留下已解析记录的退避。

## 没做什么，以及原因

未改 prompt、ranking、问题状态机、design.md、goals.md。未改 deliver.go：同一规则的写回只在 saveReminder。未发真实消息。读失败期间投出的新问题不另存一份账本。

## 接口请求

无。

## 已知问题

读失败时这次新问题只留在当前进程。账本恢复可读后，下一次进程可能再投一次该问题。

## 修复记录

账本读取失败不是空账本，不能覆盖其他问题的去重与退避 → internal/notify/reminder.go:61 internal/notify/reminder.go:82 → TestReminderLedgerFormatFaultStillDelivers
