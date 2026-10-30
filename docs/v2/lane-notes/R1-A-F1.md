# R1-A-F1

## 做了什么
`objective add` 对可补全和可识别字段软处理：缺 title 写入占位标题并将 status 设为 `paused`，用现有 `Question`（kind `custom`）记下原因。`mode`、`freeze-mode`、`status` 按大小写折叠；空值默认为 `auto`、`safe`、`active`。无法识别的枚举改为合法默认、status `paused`，原始 token 和原因写入同一问题。`--project` 仍必填并退出 2。`project add` 只强制 `--repo`，`card add` 已符合同一规则，均未改。

## 没做什么，以及原因
未实现 pause、resume、set 和 `question` 命令。未改 store 的枚举校验，也未改 `design.md` 与 `goals.md`。

## 接口请求
无。

## 已知问题
无。

## 修复记录
创建入口对可补全或可规范化字段补默认并折叠，不硬拒绝 → cmd/cardex/data.go:175, cmd/cardex/data.go:470 → TestObjectiveAddSoftInputs
无法识别的枚举不作为创建失败，暂停并在问题里保留原值和原因 → cmd/cardex/data.go:185, cmd/cardex/data.go:228 → TestObjectiveAddSoftInputs
