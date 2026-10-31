# R2-A-F1

## 做了什么
管道和 PTY 每收到一块执行器输出就追加进 `output.log`，用量解析和错误分类仍用同一份完整字节。finish 与 failStart 改用有界 context：run 取消不会取消它，workspace manager 仍在。提示文件在 run 已取消时打开失败，失败启动也会落库。注册表测试只要求 fake、claude、codex、opencode。

## 没做什么，以及原因
未改 Reconcile，也未改启动前的 needs_input 交接。未改 `registry.go`，未注册 grok、cursor、kimi、agy。未跑真实 CLI，未改路由、收割、model、store、design。

## 接口请求
以下生产文件已在本 lane 落地，本次不改，请集成者接收：

- `internal/runner/classify.go`
- `internal/runner/engines.go`
- `internal/runner/fake_child.go`
- `internal/runner/isatty_darwin.go`
- `internal/runner/isatty_linux.go`
- `internal/runner/isatty_other.go`
- `internal/runner/paths.go`
- `internal/runner/registry.go`
- `internal/runner/usage.go`
- `internal/runner/api.go` 的 `RunResult` 字段 `TailAfter`、`Tail`、`Hold`

## 已知问题
取消 run 时 `armGroup` 也会把 fired 置上，finish 因此把错误类标成 timeout。本次只修落库，未改这个分类。

## 修复记录
已接收输出在尝试仍在运行时进入 output.log，解析和分类仍看完整输出 → internal/host/run.go:153, internal/host/run.go:221, internal/host/run.go:536 → TestAttemptLogFlushedWhileRunning
取消执行不取消结果落库，收尾 context 有界且仍绑定本次 workspace manager → internal/host/run.go:430, internal/host/run.go:434, internal/host/run.go:449, internal/host/run.go:465, internal/host/run.go:468, internal/host/run.go:512, internal/host/run.go:581 → TestCancelDoesNotDropExecutionResult
注册表测试只约束本 lane 的执行器，不要求其他执行器未注册 → internal/runner/adapters_test.go:14 → TestRegistryAndCapabilities
