# R4-B-F1

## 做了什么

导入 id 在安全字符以外仍按原文区分，显式 id 保持 `v1-<id>`。枚举遵循 v1：tasks 与 archive 只收非目录 `*.json`，workflow 跳过 `.progress.json` 和已知侧车；损坏的真实记录仍作为历史导入。v2 home 与 v1 home 为同一目录（含 `.` 与符号链接）时正式导入不建库。`legacy_source` 事件记下含 v1 home、能打开原文件的路径，`Legacy.SourcePath` 仍是相对路径。负数 step 保留原值并按 0 切分。`objective set` 的未知 mode、freeze-mode，以及 `channel set` 无法解析的 window、无法识别的 runner 和 health，保留其余有效字段并报告，退出码 0。doctor 在 PATH 查找 `grok`、`cursor-agent`、`kimi`，`codex`、`claude`、`agy`、`opencode`、`git`、`sandbox-exec` 不变，只查找不启动。

## 没做什么，以及原因

没有改 `model.Card`、通知 origin 或 `positioning.Reject`，这些包不在本 lane 写域。没有改 `internal/e2e`，S21 仍比较相对 `Legacy.SourcePath`。没有正式导入 `~/.cardex`。

## 接口请求

`model.Card` 仍没有 origin 字段，卡 origin 只记在事件上。通知投递仍只用目标 origin（`internal/question`）。`positioning.Reject` 仍返回 unspecified，不改当前定位；CLI 只交出这个错误。请 `internal/model` 增加卡 origin，`internal/question` 用它投递，`internal/positioning` 实现拒绝。

## 已知问题

doctor 仍不把未知重置规则当问题，quota 会把未知规则收成 rolling。

## 修复记录

N1：不同原文的稳定 id 必须不同，显式 id 仍是 `v1-<id>` → internal/importer/place.go:269 → TestImportDistinctStableIDs
N2：已给出的非规范业务值软处理，保留其余有效字段并报告 → cmd/cardex/ops.go:116 cmd/cardex/surface.go:556 → TestSoftBusinessValues
N3：doctor 查找执行器实际二进制且只查 PATH → cmd/cardex/doctor.go:88 → TestDoctorExecutorNames
N4：来源要能打开原文件并带上 v1 home，相对 SourcePath 保持不变 → internal/importer/import.go:303 → TestImportSourceLocatesFile
N5：按 v1 区分记录和侧车，损坏的真实记录仍导入并计数 → internal/importer/import.go:104 → TestImportSkipsWorkflowSidecars
N6：卡 origin 仍只在事件，通知仍用目标 origin，positioning reject 仍 unspecified，CLI 不另做 → cmd/cardex/origin.go:27 internal/question/service.go:48 internal/positioning/home.go:344 → TestPositioningApproveUsesStore
N7：正式导入在 v2 与 v1 为同一目录时不在源目录建库 → internal/importer/import.go:71 → TestImportRefusesSourceDatabase
N8：负数 step 保留原值并按 0 切分，不把正文全部标成已完成 → internal/importer/place.go:161 → TestImportContinuableBodyKeepsStepBoundaries
