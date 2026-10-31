# R3-C

## 做了什么
每个目标一条 `merge.Service`：临时 worktree 里把卡分支三方合并到集成分支 HEAD，跑 acceptance 和 `test_cmd`。通过则快进（旧 HEAD 是祖先），删除 worktree，保留分支。同一目标串行。冲突不移动 HEAD，派 `rb-` rebase，正文含冲突路径和冲突块。验收或 `test_cmd` 变红不移动 HEAD，派 `rp-` repair，正文含失败输出。二者都在原卡分支上续做。
`sched.Runner.Tick` 走设计 §6.1 的八步：`host.Reconcile`；harvest 后处置；仅 `mode=auto` 调 `designStep`/`devStep`；每目标一步合并；派发 ready 卡（claims 冲突则 waiting，原因写明占用卡和符号；否则 `route.Select`、`workspace.Create`、写 prompt，并以新进程组启动 host）；余量填充（重置在 2 小时内且剩余高于红线加 20%，且本轮没有别的 ready 卡需要该渠道）；`config.json` 的 schedules 到期入队一次；对待投递问题调用 notify。`mode=manual` 不自动生成，派发、复核和合并照常。没有新事实时再 Tick 不会多一次尝试、快进、定时卡或通知。
`sched.Serve` 循环 Tick 并挂 `board.Handler`。取消后不杀掉仍活着的 host。S02、S03、S04、S05、S19、S20 走这条调度器。
cron-lite 是五段「分 时 日 月 周」。每段只能是 `*` 或一个整数，周日是 0。没有范围、列表、步长或名字。`every` 按时间槽去重，同一槽只入队一次。

## 没做什么，以及原因
未实现设计 §6.2–§6.6。未重写路由、看板、通知适配器或 prompt 模板。`cardex host` 和 `cardex schedule` 的 CLI 文案保持原样。未改 `internal/model`，未加依赖。

## 接口请求
`cardex host <attempt-id>` 仍打印「未实现（由 R2-A 负责）」。调度器设置 `CARDEX_SCHED_HOST=1`，在 init 里调用 `host.Run(host.WithWorkspace(ctx, workspace.Git{}), home, id)`，子进程才会真的跑尝试。请把 host 子命令接到 `host.Run`，然后去掉这个环境变量。
loop 没有处置入口，`DesignStep`/`DevStep` 仍返回 ErrNotImplemented。passed 的 dev、repair、rebase、skeleton、backlog 会先入队 `rv-<id>`，正文是写入 `## 复核结论` / `pass` 的替身脚本。请给出处置函数后换掉。
`prompt.Build` 和 `notify.Stub.Deliver` 仍是 ErrNotImplemented。Tick 会调用；prompt 失败时退回标题加正文，通知失败则留到下一轮。
`model.Budget` 还没有时长。调度把 `30m` 写到 `attempts/<id>/hard-duration`。请按 R2-A 的请求加上 `HardDuration`。
`Serve` 没有 io.Writer。`runServe` 除调用 `sched.Serve` 外，还把 `$CARDEX_HOME/serve-addr` 和 `serve-loop.log` 抄到原 stdout（`serve listening` 与 `serve stub-loop iteration N`），这样 `TestServeLoopsUntilStopped` 不用改断言。

## 已知问题
复核结论是固定的 pass，不是另一个模型族的真实复核。rebase 和 repair 若没有 fake 脚本或 `scripts/<id>.json`，宿主仍会启动，但工作区可能没有解冲突或修好测试，合并会再次失败。
