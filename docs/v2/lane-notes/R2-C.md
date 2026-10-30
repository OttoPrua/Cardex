# R2-C

## 做了什么
`workspace.Git` 实现 Create、Commit、Diff、Remove、BlockPush。Create 用 `git worktree add -b`，路径 `<worktree_root>/<card>-<attempt>`，分支 `cardex/<objective>/c/<card>`；新卡从集成分支 HEAD，续做从卡分支 HEAD。`.cardex-result.md` 写入该 worktree 的 `info/exclude`。Commit 执行 `git add -A`，只有结果文件以外的改动才提交。Remove 删除 worktree 并保留分支。BlockPush 打开 `extensions.worktreeConfig`，把 `core.hooksPath` 指到模块内 `internal/workspace/hooks/pre-push`，并把 `remote.*.pushurl` 设为无效地址。
`claims` 用 `go/parser` 提取顶层函数、`Type.Method` 和类型。ChangedSymbols 对比两个提交：Go 符号、新建文件按路径、非 Go 按文件。Conflicts 只做调度检查，原因形如 `被卡 X 占用 f.go:Foo`。
`harvest.Harvest` 按 §5 读退出码、尽力读事件流、算 diff 和实际符号、在 worktree 里跑带超时的 acceptance，并宽松解析 `.cardex-result.md`（标题空白/大小写、JSON 围栏/注释/尾逗号）。超出声明不算失败，只返回要更新的声明。
取消 S06 的 skip，由测试直接调用 `harvest.Harvest`。

## 没做什么，以及原因
未做调度、路由、合并队列，也未执行 §5 的处置列；这些属于后续 lane。未改 `internal/model` 和 `go.mod`，未引入 tree-sitter 或其他依赖。未做 prompt、看板、沙箱和非 Go 符号提取。

## 接口请求
`model.Claim` 没有目录位。现用路径以 `/` 结尾表示目录声明：它与该目录下空 symbol 的路径冲突，不与带 symbol 的声明冲突。空 symbol 无法区分「新建文件」和「已有文件的文件级锁」。若要同时表达，给 Claim 增加种类字段即可。

## 已知问题
acceptance 在 context 没有 deadline 时默认 2 分钟。Git 不读 linked worktree 自己的 `info/exclude`（`info` 在 common dir），因此额外把该文件设为这个 worktree 的 `core.excludesFile`。`extensions.worktreeConfig` 写在公共 config；pushurl 和 hooksPath 只写 worktree 本地配置。
