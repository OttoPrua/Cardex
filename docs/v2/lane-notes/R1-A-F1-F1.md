# R1-A-F1-F1

## 做了什么
`objective add` 把暂停目标的 `CreateObjective` 和记录异常输入的 `CreateQuestion` 包进现有 `db.WithTx`。缺标题、无法识别的 mode、freeze-mode、status 仍暂停并写一条 `custom` Question；Question 写入失败时目标一并回滚。

## 没做什么，以及原因
未改软输入规则（大小写折叠、空默认、仅缺 `--project` 硬拒绝）。`card add` 把原因写在卡的 `WaitReason`，一次 `CreateCard` 完成，不是本缺陷。未改 store、schema、`design.md`、`goals.md`，也未实现 pause、resume、set 和 `question` 命令。

## 接口请求
无。写域内搜过 `CreateObjective` 与 `CreateQuestion`：生产路径只有 `cmd/cardex/data.go` 的 `objective add`。没有写域外的「暂停记录 + Question」成对写入。

## 已知问题
无。

## 修复记录
暂停目标与保存异常输入的 Question 必须同一事务提交，后者失败则两者都不留 → cmd/cardex/data.go:229 → TestObjectiveAddQuestionCommit
