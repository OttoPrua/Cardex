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

Goal 要求两次清点完全一致，此项放弃：v1 自己的 tick 改写了 `~/.cardex`（events、manager-wake、`.lock`），起止清点的文件数、总字节和最新 mtime 不一致。本 lane 没有停该进程，也没有回写这个目录。workflow 的 owner_choice、writing、locally_integrated 仍是 unparsed。
