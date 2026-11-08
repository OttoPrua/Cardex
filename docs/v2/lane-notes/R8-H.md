# R8-H

## 做了什么
CLI `resolveHome` 顺序仍是 `--home`，否则 `CARDEX_HOME`，否则 `UserHomeDir()/.cardex`；绝对路径后 `homeguard.Check`，再 `MkdirAll`。看板 `internal/board/api.go:286` 改为同一顺序，空 home 落到用户目录的 `.cardex`（292–296），然后做同一 v1 检查，不 `MkdirAll`。v1 仍是 `tasks/` 里有非目录 `.json` 且没有 `cardex.db`，错误含 `import-legacy`。
改了 `cli_test.go` 两处。`TestRefusesUserCardexHome`（834）先 `Setenv("HOME", t.TempDir())`，在该临时目录的 `.cardex` 放 `tasks/*.json`、不放 `cardex.db`，再把这个临时路径交给 `Main`，断言拒绝且错误含 `import-legacy`。不再读取真实 `UserHomeDir()/.cardex`。`TestServeLoopsUntilStopped`（789）对 `127.0.0.1:8787` 和 `:8788` 仍用 v1 夹具，在 listen 前返回且不写 `serve-addr`；循环等待用 `127.0.0.1:0`，日志只匹配 `loop iteration`（812）。
第一次测试曾经监听 `127.0.0.1:8788`。这次返工不绑定 8787 或 8788。
`tasks/`：v2 不在 home 下创建。`internal/importer/import.go` 的 `listV1`（104–106）只读 v1 来源的 `tasks/` 与 `archive/`。
rg `UserHomeDir` `-g '*_test.go'`：`cmd/cardex/cli_test.go` 无命中。唯一剩余是 `internal/runner/grok_test.go:1547`，只确认 kimi 配置 home 不是真实目录下的 `.kimi-code` 或 `.cardex`，不把该路径交给被测入口。生产代码仍用用户目录的 `.cardex` 作为默认 home。`rg` 在 `cmd`、`internal/board`、`internal/homeguard` 里没有 `reservedPort`、`refusing to use ~/.cardex` 或 `missing --home or CARDEX_HOME`。

## 没做什么，以及原因
未改导入映射、调度、派发、`internal/sched` 和 `design.md`。`cli_test.go` 只改上述两个测试。`internal/runner/grok_test.go` 在写域外，未改。未 push。

## 接口请求
无。

## 已知问题
与 R8-L 合并 `cli_test.go` 时，目录隔离和 8787/8788 不 listen 以本 lane 为准；循环日志子串 `loop iteration` 两边都能过。`docs/v2/design.md` 第 1 节仍写 `~/.cardex/v2`，本 lane 不改。

## 修复记录
测试先把 HOME 指到临时目录，不得把 UserHomeDir()/.cardex 交给被测入口；8787/8788 在 listen 前返回且不写 serve-addr → cmd/cardex/cli_test.go:834、cmd/cardex/cli_test.go:791 → TestRefusesUserCardexHome、TestServeLoopsUntilStopped
看板默认 home 与 CLI 相同（--home，否则 CARDEX_HOME，否则用户目录的 .cardex），再做 v1 检查 → internal/board/api.go:286 → TestResolveHomeUsesCLIOrder、TestResolveHomeRefusesV1DefaultHome、TestServeDefaultHomeBoard
lane note 按最终 diff 写明改了哪两个测试，并写明第一次监听过 8788、返工后不绑定 8787/8788 → docs/v2/lane-notes/R8-H.md:5、docs/v2/lane-notes/R8-H.md:6 → TestRefusesUserCardexHome、TestServeLoopsUntilStopped
