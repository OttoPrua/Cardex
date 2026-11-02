# R4-INT

## 做了什么
同一包里重复的 `authClassRe`、`permissionClassRe`、`timeoutClassRe` 只留 `classify.go` 的一份，`jsonString` 只留 `usage.go` 的一份。两份文本相同。`ClassifyOutput` 与 `classifyScanned` 仍用这三组模式，agy 与 usage 仍用同一个 `jsonString`。`TestS23` 和 `TestRankingDefaultEntry` 仍自己吸收 ranking；`sched.New` 已安装的 `Dispose` 留在生产构造里，场景把它卸下，收割保持未消费，吸收之后 tick 才能继续。

## 没做什么，以及原因
findings 没有剩余分支，没有再 merge。没有改 `docs/v2/goals.md` 和 `docs/v2/design.md`。没有接 R4-C-F1，没有让 `absorbAttempt` 吸收 ranking，没有 push。

## 接口请求
无。

## 已知问题
S23 仍直接调用 `AbsorbHarvestedRanking`，生产 `Dispose` 不吸收 ranking。其他 e2e 场景保持原 skip。darwin 与 linux 的 pty、isatty、sandbox 包装由构建标签成对提供，不是同一构建里的重复声明。

## 修复记录
同一包内每个标识符只保留一份定义，文本相同就删副本，调用方结果不变。 → internal/runner/classify.go:16, internal/runner/classify.go:17, internal/runner/classify.go:18, internal/runner/usage.go:172 → TestDedupedRunnerHelpers
本 worktree 的 go build、go vet、go test 必须通过且改动已提交。 → internal/e2e/scenarios_test.go:1361, internal/runner/dedup_test.go:12 → TestDedupedRunnerHelpers, TestS23, TestRankingDefaultEntry；证据 go build ./... && go vet ./... && go test ./...
