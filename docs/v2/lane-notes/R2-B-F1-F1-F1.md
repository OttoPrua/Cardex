# R2-B-F1-F1-F1

## 做了什么
完整 JSON 文档里的嵌套 goal_updated 不再当作独立 JSONL 记录。预算分类和 updates.jsonl 终态只读文档顶层的 params.update；不是一份完整文档时仍按 JSONL 走。已知提示回显仍排除。

## 没做什么，以及原因
未改 classify.go、runner.go、fake.go、Build、Drive、WritablePaths、Capabilities。未改 goals.md、design.md。未跑真实 CLI，未做合并。Cursor、Kimi、AGY 已走完整文档扫描，本次只补了同一反例。

## 接口请求
无。

## 已知问题
classify.go 的 jsonObjects 仍按行解码。完整文档里的嵌套 type=budget_limited 行不在本 lane 写域。

## 修复记录
嵌套在一份完整 JSON 文档内的事件不是独立 JSONL 记录，预算状态解析遵守同一规则 → internal/runner/grok.go:1198、grok.go:1239、grok.go:1317、grok.go:1339 → TestNestedBudgetIsNotJSONLRecord
