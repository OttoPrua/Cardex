# R8-INT

## 做了什么
用 `git merge --no-ff` 合入 `v2-R8-L`。第一父提交 `14e0699`，第二父提交 `b94968d`。唯一内容冲突是 `cmd/cardex/cli_test.go`：8787 与 8788 仍在 listen 前因 v1 home 拒绝，stderr 含 `import-legacy`，不含端口保留拒绝，也不写 `serve-addr`。`TestRefusesUserCardexHome` 仍把 `HOME` 设为临时目录，期望 `import-legacy` 且不创建 `cardex.db`。循环计数改为完整短语 `serve loop iteration`。

## 没做什么，以及原因
没有合入其他分支，没有 push，没有改 `docs/v2/goals.md`，也没有改 `docs/v2/design.md` 里 `v2-R8-L` 已经提交的内容。没有重做集成分支创建、`launch_failed` 路线复用或 serve 容错。`go build`、`go vet`、`go test` 在合并后一次通过，没有再改生产和测试代码。

## 接口请求
无。

## 已知问题
仓库里合并前已有的冲突标记字面量仍在：`internal/merge/queue.go:433`、`internal/merge/queue.go:440`、`internal/merge/queue_test.go:167`、`internal/sched/loop_test.go:464`、`internal/e2e/scenarios_test.go:415`。它们是检测或夹具，不是未解决冲突，本次不改。

## 修复记录
按顺序 no-ff 合入 findings 点名的分支，冲突保留双方意图。 → cmd/cardex/cli_test.go:791 → TestServeLoopsUntilStopped, TestRefusesUserCardexHome
隔离真实目录且 8787/8788 不 listen 以 R8-H 为准，循环日志子串以 R8-L 的 serve loop iteration 为准。 → cmd/cardex/cli_test.go:791, cmd/cardex/cli_test.go:798, cmd/cardex/cli_test.go:801, cmd/cardex/cli_test.go:812, cmd/cardex/cli_test.go:834, internal/sched/serve.go:83 → TestServeLoopsUntilStopped, TestRefusesUserCardexHome
本 worktree 的 go build、go vet、go test 必须通过且改动已提交。 → cmd/cardex/cli_test.go:812, cmd/cardex/cli_test.go:834 → TestServeLoopsUntilStopped, TestRefusesUserCardexHome
