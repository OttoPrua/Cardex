# R5-B
## 做了什么

v1 卡的 `goal` 为对象时按状态落位，不再整卡 unparsed。两次 dry-run 相同：canceled 654、done 2545、failed 19、held 465→paused、running 3→ready、unparsed 75→history。导入两次都是 3761 张卡、143 个 manual「v1 导入」，卡和目标的 Channel/Model 都空。queued 为 0，不在表里。

| id | v1 | v2 | mode | pin | route | reason | src |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v1-t1101-1821-7438 | running | ready | manual | unpinned | frozen | no | tasks/t1101-1821-7438.json |
| v1-t1105-0420-a623 | running | ready | manual | unpinned | pref | no | tasks/t1105-0420-a623.json |
| v1-t0818-0330-c4dd | held | paused | manual | unpinned | pref | yes | tasks/t0818-0330-c4dd.json |
| v1-t0807-0223-af7d | held | paused | manual | unpinned | pref | no | tasks/t0807-0223-af7d.json |
| v1-t1011-0030-6a30 | held | paused | manual | unpinned | frozen | yes | tasks/t1011-0030-6a30.json |
| v1-t0915-2318-dce5 | held | paused | manual | unpinned | pref | no | tasks/t0915-2318-dce5.json |
| v1-t0812-1444-5ae2 | done | done | manual | unpinned | pref | no | tasks/t0812-1444-5ae2.json |
| v1-t0706-0203-0263 | done | done | manual | unpinned | pref | no | archive/t0706-0203-0263.json |
| v1-t1011-0230-44bc | done | done | manual | unpinned | frozen | no | tasks/t1011-0230-44bc.json |
| v1-t0705-2229-345b | done | done | manual | unpinned | none | no | archive/t0705-2229-345b.json |
| v1-t1025-0859-9002 | done | done | manual | unpinned | frozen | no | tasks/t1025-0859-9002.json |
| v1-t0820-1230-76fa | failed | failed | manual | unpinned | pref | no | tasks/t0820-1230-76fa.json |
| v1-t0807-0048-bca1 | failed | failed | manual | unpinned | pref | no | archive/t0807-0048-bca1.json |
| v1-t1011-1750-6831 | failed | failed | manual | unpinned | frozen | no | tasks/t1011-1750-6831.json |
| v1-t1005-1836-a8f9 | failed | failed | manual | unpinned | pref | no | tasks/t1005-1836-a8f9.json |
| v1-t0709-1847-2de2 | canceled | canceled | manual | unpinned | pref | no | archive/t0709-1847-2de2.json |
| v1-t1012-1256-1a0d | canceled | canceled | manual | unpinned | frozen | no | archive/t1012-1256-1a0d.json |
| v1-t0705-2226-0ce7 | canceled | canceled | manual | unpinned | none | no | archive/t0705-2226-0ce7.json |
| v1-t1005-0319-dd5b | canceled | canceled | manual | unpinned | pref | no | archive/t1005-0319-dd5b.json |
| v1-wf0914-0254-a336d2 | unparsed | canceled | manual | unpinned | none | yes | workflows/wf0914-0254-a336d2.json |

## 没做什么，以及原因

未改 importer 以外的包，未做 §14 切换，未把根因分类写进导入器，未逐条迁事件，未复活 held。根因表是 2026-10-30 对 tasks 目录的分类，报告不输出，合计 452=held 437+failed 15：stream_incomplete 63；invalid_terminal_result 34；零事件退出 24；终局缺失或其他 32；人工封存 94；收割未过 55；集成门 53；权限拒绝 28；硬用量限额 13；budget_limited 12；手动 Goal 11；认证 10；用量样本缺失 10；传输失败 7；设计结果收成 failed 4；超时 2；模型 refusal 0；写域或租约 0；全局 pause 0；依赖未满足 0。quota 的 failed 19 = tasks failed 15 + archive failed 4。quota 3643 张 tasks+archive，dry-run 时 3686（+43）。状态 quota→dry-run：done 2532→2545，canceled 654→654，held 437→465，failed 19→19，running 1→3，queued 0→0。无 frozen_route：done 2490→2500，canceled 648→648，held 378→385，failed 17→17。有 frozen_route 110→134。created_at≥2026-10-31 的新卡 36（held 22、running 3、done 11，frozen 18）；2026-10-30 当天 11 张（held 9、done 2，frozen 10），快照钟点未知。去掉 10-31 及以后 36 张后，更早的卡净 +7：running 1→0，held +6，done +2，frozen 110→116。75 份 workflow 不在 3643 里：owner_choice 57、writing 11、locally_integrated 7，不是卡状态，落成 unparsed/history。导入比 dry-run 晚一步，v1 把一张 running 收成 done：running 3→2，done 2545→2546。

## 接口请求

- `internal/board/data.go` 的 `legacyImported` 把带 legacy 状态或来源的卡全部移出默认视图，ready 的 running 导入卡也不在 `/` 和 `/api/work`。本 lane 不改 board。
- `internal/sched/loop.go` 的 `canDispatch` 不因 `mode=manual` 或 legacy 跳过 ready。本次 home 没有 channel，Tick 没有 attempt，也没有 host。两次 curl 之间 Tick 入队了 `sch-ranking-2965`。不要在本 lane 改 sched。

## 已知问题

演练窗口里 v1 自己的 tick 改写了 `~/.cardex`（events、manager-wake、`.lock`），起止清点的文件数、总字节和最新 mtime 不一致。本 lane 没有写这个目录。workflow 的 owner_choice、writing、locally_integrated 仍是 unparsed。

## 这次观察

证据在 `/Volumes/Storage/Development/Cardex/cache/cardex-v2-import-rehearsal/evidence/`。源目录调用只有 `internal/importer/import.go:26` Stat、`:133` ReadDir、`:158` ReadFile、`:226` ReadFile、`:345` Stat。`:338` 的 Stat 是 v2 home。生产代码没有源写入。先前 `/var/folders/.../grok-goal-97e62b3682ae/implementer/` 里的报告、curl 状态码和 go test 输出都还在，已复制到 `evidence/prior-rehearsal/`（38 个文件，无缺失）。该目录里的 `cardex` 二进制和 `serve.pid` 不是报告、curl 或测试输出，未复制。

只读清点后，对 `~/.cardex` 做了一次 `--dry-run` 和一次导入，v2 home 是 `observe-r5b-rework`。两份清点是 `inventory-before.txt`、`inventory-after.txt`，差异是 `inventory-diff.txt`。`inventory-class.txt` 为 A，差异 0 条，没有 `cardex.db`，没有 `v2/`。dry-run 与导入的 v1 计数相同：canceled 654、done 2580、failed 19、held 470、running 2、unparsed 75。tasks+archive 为 3725，比 3643 多 82。75 份 workflow 不在 3643 里：owner_choice 57、writing 11、locally_integrated 7，即这 75 条 unparsed。导入写入新 home 的 3800 张卡，等于 3725+75。

相对 facts-quota-routing 的 done 2532、canceled 654、held 437、failed 19、running 1：done +48、held +33、running +1，canceled 与 failed 为 0，48+33+1=82。created_at≥2026-10-31 的新卡 75，都在 tasks：done 46、held 27、running 2，frozen 23。2026-10-30 当天 11 张：held 9、done 2，frozen 10，快照钟点未知。2026-10-30 之前 3639 张：done 2532、canceled 654、failed 19 持平，held 434（少 3），running 0（少 1），合计少 4。10-30 与更早合计 3650，比 3643 多 7：done +2、held +6、running -1。7+75=82。

frozen_route 139，比 110 多 29。10-30 之前 106（少 4），当天 10，10-31 及以后 23。10-30 与更早合计 116（多 6），6+23=29。archive 的 6 张 frozen 都在 10-30 之前。452=tasks held 437+tasks failed 15。现在 tasks held 470、failed 15，合计 485，多 33。failed 15 都在 10-30 之前。held 的 33=旧卡少 3、当天 9、10-31 及以后 27。archive failed 仍是 4，所以 failed 合计仍是 19。

## 修复记录

导入只读源，源上只有 Stat、ReadDir、ReadFile → `internal/importer/import.go:26` Stat、`:133` ReadDir、`:158` ReadFile、`:226` ReadFile、`:345` Stat，无源写入可删 → `TestImportReadOnlySource`
差异只能落在 v1 的 control/、events/、tasks/、logs/、usage.json、retro_counter.json、.lock，且不能是 cardex.db 或 v2/ → 未改代码；`evidence/inventory-class.txt` 结果 A，差异 0 → `TestImportReadOnlySource`
已完成的幂等、20 张抽查、8791 和 go test 不重做，临时目录里的报告要留下 → `evidence/prior-rehearsal/` 38 个文件，无缺失 → 不新跑
与 3643、110、452 的差额按 v1 运行期间的新增和状态变化逐项给数字 → 本节「这次观察」；计数见 `evidence/dry-run.txt`、`evidence/import.txt`、`evidence/gap-scan.txt` → 不新跑
