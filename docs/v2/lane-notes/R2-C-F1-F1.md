# R2-C-F1-F1

## 做了什么
BlockPush 不再给 https、http、ftp、ftps 写 url.*.insteadOf。这四类地址的 fetch 和 ls-remote（含 ls-remote --get-url）保持调用方原来的地址。推送仍拒绝：空的 pushInsteadOf、无效 pushurl、receivepack、拒绝一切的 pre-push hook，以及非布尔的 push.followTags。http(s) 与 ftp(s) 不吃 receivepack，显式 pushurl 也不吃 pushInsteadOf，所以由 push.followTags 在传输前拒绝继承和直接地址，包括 --no-verify。主仓库的推送配置不改，也不设置 protocol.allow。

## 没做什么，以及原因
未改 internal/model、go.mod、design.md、goals.md。未做调度、路由和合并队列，未合并其他分支。未改 internal/e2e 的 skip，未改 docs/v2/lane-notes/R2-C-F1.md。未架设 FTP/FTPS 服务；读取回归只核对解析出的地址。

## 接口请求
无。写域 internal/workspace、internal/claims、internal/harvest 里，会改写 fetch 或 ls-remote 地址的只有上述 insteadOf，已删除。没有需要写域外修改的接口。

## 已知问题
acceptance 无 deadline 时默认 2 分钟。linked worktree 不读自己的 info/exclude，仍改用 core.excludesFile。extensions.worktreeConfig 写在公共 config。

## 转交
PTY Drive 错误必须记在该次尝试上：host_finish 与错误摘录都含该错误（prompt missing），同次或之后写出的执行器字节（completion）留在尝试日志，不能丢掉。写域 internal/workspace、internal/claims、internal/harvest 不读 Drive、不写 host_finish、不改尝试日志，无违反位置。internal/host/run.go:283 在超时且错误为 context.Canceled、io.ErrClosedPipe 或 os.ErrClosed 时清掉 driveErr；internal/host/run.go:408 与 internal/host/run.go:446 只在 driveErr 非空且不是 context.Canceled 时写入摘录和 host_finish。internal/runner/fake.go:166 在行前缀 drive_error: 或行等于 completion 时返回。请 host/runner 的 lane 修 TestPTYDriveErrorIsApplied。本 lane 未改这些文件。

## 修复记录
防推送措施不能改写正常读取所用的地址 → internal/workspace/git.go:244, internal/workspace/git.go:258 → TestBlockPushKeepsReadAddress
PTY Drive 错误必须写入该次尝试的 host_finish 与错误摘录，同次写出的执行器字节留在尝试日志 → 写域无违反位置，转交 internal/host/run.go:283、internal/host/run.go:446、internal/runner/fake.go:166 → TestPTYDriveErrorIsApplied
