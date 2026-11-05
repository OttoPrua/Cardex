# R2-B-F1-F1

## 做了什么
嵌套 error 对象取 message、msg、text、type 的首个非空值，空白跳过，不拼接。完整 JSON 文档只读顶层 error 与 usage；不是一份完整文档时仍按 JSONL 解析，已知提示回显仍排除。Grok、Cursor、Kimi、AGY 的 Classify 与 ParseUsage 都走这两处。

## 没做什么，以及原因
未改 classify.go、runner.go、fake.go、R2-A 适配器、model、go.mod、goals.md、design.md。未改 Build、Drive、WritablePaths、Capabilities，未解析 Kimi token，未跑真实 CLI，未做合并。

## 接口请求
classify.go 的 nestedMessage 只读 message 再 name。R2-A 的 type=error 与 turn.failed 因此看不到 msg、text、type。最小改法：改为这四字段的首个非空值。该文件不在本 lane 写域。

## 已知问题
无。

## 修复记录
嵌套 error 取 message、msg、text、type 的首个非空值，不拼接同级字段 → internal/runner/grok.go:839 → TestNestedErrorFallbackAndWholeDocument
完整 JSON 文档只消费顶层 error 与 usage，嵌套对象不算 JSONL → internal/runner/grok.go:410、grok.go:432、grok.go:1355 → TestNestedErrorFallbackAndWholeDocument
