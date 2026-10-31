# R2-C-F1

## 做了什么
Git 已记录、文件字节相同的改动仍是一条文件级声明。提交对比和工作区对比都走 `claimsFromStatus`。目录声明只覆盖新建文件：`Conflicts` 另收 `exists`，已有文件不再被目录挡住。BlockPush 仍把 pre-push hook 指到拒绝脚本，并把 pushurl 设为无效地址。空的 pushInsteadOf 改写 `.`、相对路径和其它直接推送。显式 pushurl 不吃这条改写，http(s) 也不吃 receivepack，所以 insteadOf 改写 https、http、ftp、ftps，避免连上这些地址。本地继承的 pushurl 仍由 receivepack 拒绝。不设置 protocol.allow，fetch 和 ls-remote 可用。

## 没做什么，以及原因
未改 internal/model 和 go.mod。未做调度、路由、合并队列，也未改 design.md 或 goals.md。未处理这三条以外的问题。

## 接口请求
model.Claim 仍无种类字段。空 symbol 只表示整文件，不能表示新建。Conflicts 增加 exists func(path string) bool，返回 false 才视为新建。集成者给 Claim 加种类字段后可去掉这个旁路。

## 已知问题
acceptance 无 deadline 时默认 2 分钟。linked worktree 不读自己的 info/exclude，仍改用 core.excludesFile。extensions.worktreeConfig 写在公共 config。https、http、ftp、ftps 的 fetch 和 ls-remote 仍被 insteadOf 改写到无效地址，本次不修。

## 修复记录
文件内容不变不能据此忽略 Git 已记录的模式变化。 → internal/claims/symbols.go:170 → TestModeOnlyChangeIsClaim
目录声明只能覆盖新建文件，不能阻塞已有文件的文件级声明。 → internal/claims/conflicts.go:108 → TestConflictsDirectoryOnlyNewFiles
防推送措施不应连带禁止正常的 Git 读取。 → internal/workspace/git.go:232, internal/workspace/git.go:240, internal/workspace/git.go:259 → TestBlockPushBlocksInheritedPushURLs
解除读取限制后，更长的继承或进程级 pushInsteadOf 仍不能让直接地址的 push --no-verify 更新本地仓库。 → internal/workspace/git.go:256, internal/workspace/git.go:259, internal/workspace/git.go:262 → TestBlockPushRefusesLongerPushInsteadOf
