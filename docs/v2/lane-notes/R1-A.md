# R1-A

## 做了什么
清空 v1，保留 `.gitignore` 与 `LICENSE`。新增 model、SQLite store、api.go 桩、fake 执行器、e2e 与跳过的 S01–S24，以及第 12 节 CLI。project、objective、card 的 add/list/show 已接 store。

## 没做什么，以及原因
未实现调度、收割、合并、路由、额度、分级、看板、导入、通知。业务按 lane 分工留给后续，本 lane 只定接口。

## 接口请求
无。

## 已知问题
无。

## 接口到章节
| 接口 | 章节 |
| --- | --- |
| runner | §5/§13 |
| host | §2/§12 |
| pty | §2 |
| sandbox | §13 |
| workspace | §4 |
| claims | §4 |
| harvest | §5 |
| route | §7.1 |
| quota | §7.2–§7.3 |
| classify | §7.4 |
| positioning | §10 |
| loop | §6 |
| question | §9 |
| merge | §4 |
| sched | §6.1 |
| prompt | §8 |
| notify | §9 |
| board | §11 |
| importer | §14 |
| scorecard | §10 |
| e2e | R1 测试支撑 |
