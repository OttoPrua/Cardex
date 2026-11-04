# R5-A

## 做了什么
dev home 写入 grok、codex、claude、cursor、kimi、agy、opencode。模型来自各 CLI 当前配置；cursor-agent 符号链接断开，composer-2 不是登录读数，health=degraded。doctor 只报 cursor-agent 不在 PATH。sample-repo 的 `go test ./...` 通过。八张 T1 卡与 fast auto 目标 cli-sub 已交给 serve。`127.0.0.1:8790` 上 `/` 与 `/channels` 各 curl 两次，均为 HTTP 200。

| 渠道 | 命令 | 沙箱 | 用量 | 退出/outcome | 耗时 |
| --- | --- | --- | --- | --- | --- |
| grok | 无 argv；runner grok is not registered | 未进入 | 0，未解析 | 无退出码 / no_output | 4s |
| codex | `codex exec --sandbox workspace-write -m gpt-6.1-sol` | sandbox-exec 拒绝 apply | 0，未解析 | 71 / no_output | 34ms |
| claude | `claude -p --output-format json --dangerously-skip-permissions` | 同上 | 0，未解析 | 71 / no_output | 37ms |
| cursor | health 放开 pin，未执行 cursor-agent | 改派后同样拒绝 | 0，未解析 | 71 / no_output | 33ms |
| kimi | 无 argv；runner kimi is not registered | 未进入 | 0，未解析 | 无退出码 / no_output | 2s |
| agy | 无 argv；runner agy is not registered | 未进入 | 0，未解析 | 无退出码 / no_output | 7s |
| opencode | `opencode run --pure --format json --auto` | sandbox-exec 拒绝 apply | 0，未解析 | 71 / no_output | 33ms |
| grok goal_mode | 无 goal argv（无 --cwd） | 未进入 | 0，未解析 | 无退出码 / no_output | 9s |

实际总花费：解析 token 0（16 次尝试的 usage.source 都是空）。墙钟约 240s，未到 300k token 或 2 小时。

## 没做什么，以及原因
执行器没有改到仓库。四家未注册；claude、codex、opencode 在 sandbox_apply 失败，本会话里空策略的 sandbox-exec 也是 exit 71。cli-sub 没有设计、拆卡、并行开发、复核、合并，也没有方向题。未改 internal、cmd 和各 CLI 配置，未 push，未用 8787/8788，未写 ~/.cardex。

## 接口请求
Lookup 补上 grok、cursor、kimi、agy；host 在 requires 含 goal_mode 时设置 Grok.GoalMode。DesignStep 调用已有设计步进，不要返回 ErrNotImplemented。sandbox 启动失败不要收成 no_output 再换渠道。

## 已知问题
BUG-R5A-1：复现步骤：health=ok 的 grok、kimi、agy、goal-grok 派发后，launcher.log 为 runner is not registered。怀疑的代码点：internal/runner/registry.go 与 internal/host/run.go。
BUG-R5A-2：复现步骤：t1-codex attempt 0 exit 71，excerpt 为 sandbox-exec: sandbox_apply: Operation not permitted，attempt 1 改走 claude。怀疑的代码点：internal/sandbox/wrap_darwin.go，以及 internal/loop 把启动失败当无产出。
BUG-R5A-3：复现步骤：cli-sub（auto、fast）多个 tick 后 cards 与 design_nodes 仍为 0。怀疑的代码点：internal/loop/api.go 的 DesignStep。
BUG-R5A-4：复现步骤：没有 config.json 时，requires web 的 sch-ranking-2965 挂到第一个 active 目标 smoke-agy 并 needs_input。怀疑的代码点：internal/sched/loop.go 的默认排程。
余量窗口：token 上限都是 0，余量未知，红线不生效。读到计划数字后再填 LimitTokens：claude、grok、agy 用 5h，codex 用 5h 加 weekly，kimi 用 weekly，opencode 与 cursor 用 monthly。未注册渠道 concurrency 现为 0，注册后改为 1。
