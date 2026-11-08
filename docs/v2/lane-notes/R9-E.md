# R9-E

## 做了什么
`Runner.harvest` 把已解析的 `r.Home` 传给 `harvest.Harvest`（`internal/sched/loop.go:285`）。Home 非空且 attempt id 安全时，`writeEvidence`（`internal/harvest/harvest.go:78`）写 `attempts/<id>/acceptance.log`：每条验收命令先写一行 `command`、`exit`、`duration`，再接原始输出；下一条 header 在输出不以换行结尾时另起一行，原始输出字节不变。工作区根上有 `.cardex-result.md` 才复制 `result.md`，缺文件不报错。超过 1 MiB 保留首尾各 64 KiB，中间一行 `omitted N bytes`，N 是去掉的原始字节数。写失败在 deliver 成功后只记一条 `attempt:<id>` 的 `evidence_write_failed`（`internal/sched/loop.go:313`），数据是目录路径和错误文本，不含文件正文，也不改变 outcome 或处置。摘录仍是原始输出末 2000 字；已有摘录不覆盖。
`review_verdict` 记在父卡 `card:<id>`：复核卡、复核尝试、结论、意见（超过 64 KiB 截断并标 `truncated`）、吸收后的 `rework_count`、是否打开了返工上限问题。`ReworkNote` 仍是完整的最新意见。
生产路径上的 `rework`：
- `internal/loop/dev_apply.go:111` `ActionRework`，source `tests_failed`，`question_opened` false
- `internal/loop/dev_apply.go:124` 文案前缀 `rework_count would exceed ` 的上限问题，source `tests_failed`，`question_opened` true，不增加计数
- `internal/loop/dev_apply.go:919` 复核返工（含上限），source `review`；同一次 `review_verdict` 在 884（通过）和 916（返工）
`tests_failed` 另带 `attempts/<id>/acceptance.log` 和已有摘录，不带全文。
看板 `GET /api/cards/{id}` 增加 `history`（该卡的 `review_verdict` 与 `rework`，按时间）以及每个尝试实际存在的 `output.log`、`acceptance.log`、`result.md` 相对路径。

## 没做什么，以及原因
未做设计 §17 第 4 项。未改 outcome、返工计数、状态、处置或摘录长度。未加表或列。未改 `cmd/cardex`、`internal/sched/engine.go`、`internal/model`、`internal/store/migrate.go`。
现有 prod wiring 的复核脚本始终写出 `## 复核结论` 后的 pass。假执行器没有 attempt 1，而 `internal/e2e/s25_flow_test.go` 在写域外，所以这次接线不能造出复核返工，跑完没有 `rework` 事件。

## 接口请求
`cmd/cardex/data.go` 的 `cardShow`（473）在 `--json` 时仍发出只有 `Card` 和 `Origin` 的 `cardView`。看板卡 JSON 已有 `history` 和尝试证据路径。若 CLI `card show --json` 也要这两项，在 `cardShow` 上按同一形状补上，不要新命令。

## 已知问题
`internal/sched/engine.go:169` 调用 `harvest.Harvest` 时不传 Home，这条路径不写 `acceptance.log` 或 `result.md`。
测试替身会增加 `rework_count` 且不写 `rework`：`internal/e2e/scenarios_test.go` 的 `scenarioDispose`（2089、2136），`internal/sched/loop_test.go` 的 `passDisposition`（948、995）。它们不是 `sched.New` 安装的生产处置。
