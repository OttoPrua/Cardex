# R9-E

## 做了什么
`Runner.harvest` 把已解析的 `r.Home` 传给 `harvest.Harvest`（`internal/sched/loop.go:285`）。设计错误恢复的那次收割同样传入 `r.Home`（`internal/sched/engine.go:175`）。Home 非空且 attempt id 安全时，`writeEvidence`（`internal/harvest/evidence.go:41`）按传入 home 的原值写 `attempts/<id>/acceptance.log`：每条验收命令先写一行 `command`、`exit`、`duration`，再接原始输出；有 `.cardex-result.md` 才复制 `result.md`。空值检查可以 trim，拼接不再 trim。超过 1 MiB 保留首尾各 64 KiB。写失败在 deliver 成功后只记一条 `attempt:<id>` 的 `evidence_write_failed`，不含正文，也不改变 outcome。摘录仍是末 2000 字；已有摘录不覆盖。
`review_verdict` 记在父卡 `card:<id>`。opinion 是原始「复核结论」整节，保留换行和更深层标题；没有该节就用全文。超过 64 KiB 在 UTF-8 字符边界截断并标 `truncated`。判定、`ReworkNote` 和返工上限问题文案仍走 `ReviewOf`。
生产路径上的 `rework`：
- `internal/loop/dev_apply.go:111` `ActionRework`，source `tests_failed`，`question_opened` false
- `internal/loop/dev_apply.go:124` 文案前缀 `rework_count would exceed ` 的上限问题，source `tests_failed`，`question_opened` true，不增加计数
- `internal/loop/dev_apply.go:921` 复核返工（含上限），source `review`；同一次 `review_verdict` 在 886（通过）和 918（返工）
`tests_failed` 另带 `attempts/<id>/acceptance.log` 和已有摘录，不带全文。
看板 `GET /api/cards/{id}` 和 `card show --json` 都给出该卡的 `review_verdict` 与 `rework`（按时间），以及每个尝试实际存在的 `output.log`、`acceptance.log`、`result.md` 相对路径。文本 `card show` 仍是一行。

## 没做什么，以及原因
未做设计 §17 第 4 项。未改 outcome、返工计数、状态、处置或摘录长度。未加表、列或命令。未改 `internal/model`、`internal/store/migrate.go`。
现有 prod wiring 的复核脚本始终写出 `## 复核结论` 后的 pass。假执行器没有 attempt 1，而 `internal/e2e/s25_flow_test.go` 在写域外，所以这次接线不能造出复核返工，跑完没有 `rework` 事件。

## 接口请求
无。

## 已知问题
测试替身会增加 `rework_count` 且不写 `rework`：`internal/e2e/scenarios_test.go` 的 `scenarioDispose`（2089、2136），`internal/sched/loop_test.go` 的 `passDisposition`（948、995）。它们不是 `sched.New` 安装的生产处置。

## 修复记录
B1：`card show --json` 与看板给出同一份 history 和实际存在的证据路径，文本仍是一行 → `cmd/cardex/data.go:476`、`cmd/cardex/origin.go:17` → `TestCardShowJSONMatchesBoard`
B2：`review_verdict` 的 opinion 取原始「复核结论」整节（含嵌套标题），缺节则用全文；判定和 ReworkNote 仍走 ReviewOf → `internal/loop/dev_apply.go:869`、`internal/loop/outcome_evidence.go:14` → `TestReviewVerdictStoresSectionText`
N1：设计错误恢复收割传入 runner home，不退回默认 home → `internal/sched/engine.go:175` → `TestDesignErrorHarvestWritesEvidence`
N2：证据目录用传入 home 原值拼接，只在判空时 trim → `internal/harvest/evidence.go:49` → `TestHarvestEvidenceFiles`
N3：64 KiB 截断停在完整 UTF-8 字符边界并保留 truncated → `internal/loop/outcome_evidence.go:65` → `TestReviewVerdictStoresSectionText`
保存复核意见时，``` 与 ~~~ 围栏（可带 info）内的 # 行不是章节边界，未闭合围栏一直到文末；写域内只有这一处提取器会写入 review_verdict → `internal/loop/outcome_evidence.go:26`、`internal/loop/outcome_evidence.go:68`、`internal/loop/outcome_evidence.go:82` → `TestReviewVerdictStoresSectionText`
