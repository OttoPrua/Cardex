# R8-H

## 做了什么
CLI `resolveHome` 顺序为 `--home`，否则 `CARDEX_HOME`，否则 `UserHomeDir()/.cardex`；先绝对路径，再 `homeguard.Check`，再 `MkdirAll`。v1 谓词：`tasks/` 里至少有一个非目录 `.json`，且没有 `cardex.db`。错误写明这是 v1 数据目录，并给出 `cardex --home <新目录> import-legacy <这个目录>`。
四处之前无条件拒绝 `~/.cardex` 及其子路径，现在只调用该检查：
- `cmd/cardex/main.go` `resolveHome`：去掉路径禁令，空 home 落到上述默认目录。
- `internal/positioning/home.go` `refuseHome`：空路径仍报 positioning home is required，其余只查内容。
- `internal/store/store.go` `refuseCardexHome`：查数据库文件的父目录，不看路径字符串。
- `internal/board/api.go` `resolveHome`：仍要求 `--home` 或 `CARDEX_HOME`，不 `MkdirAll`，只加内容检查。
已删除 `reservedPort`。`--addr` 默认仍是 `127.0.0.1:0`，serve 帮助改为 listen address。doctor 在非 v1 的 home 失败时建议 `pass a writable --home`；home 是 v1 时建议用 `import-legacy` 导入到新目录。`import-legacy` 不检查来源。importer 的「v2 home and v1 home are the same directory」未改。
`tasks/`：v2 不在 home 下创建。生产代码只有 `internal/importer/import.go` 的 `listV1`（104–106 行）读取 v1 来源的 `tasks/` 与 `archive/`，没有对 v2 home 的 Mkdir。
rg（42 行）：生产代码没有 `reservedPort`、`refusing to use ~/.cardex`、`outside ~/.cardex` 或对 8787/8788 的拒绝。残留不是拒绝：`docs/v2/design.md` 第 1、14 节仍是旧 home/端口；lane-rules、goals、其他 lane note 只说开发期不写该目录；`cli_test.go:790` 仍断言这两口非 0；`cli_surface_test.go:1588` 与 `internal/e2e/prod_wiring_test.go:81` 只检查实际监听地址不是这两口；loop 测试把 8787 当作事实文本。

## 没做什么，以及原因
未改导入映射、调度、派发和 design.md。未改 `cli_test.go`，它属于 R8-L。未绑定 8787/8788，未 push。

## 接口请求
请 R8-L 改 `cmd/cardex/cli_test.go`。`TestServeLoopsUntilStopped` 不要对 `127.0.0.1:8787` 和 `:8788` 调用 `runServe`：端口空闲时会进 `sched.Serve` 并绑定，直到测试结束。`TestRefusesUserCardexHome` 改为临时目录中的 v1 home，不要把 `UserHomeDir()/.cardex` 交给 `Main`。

## 已知问题
`TestRefusesUserCardexHome` 仍把真实 `UserHomeDir()/.cardex` 交给 `Main`。这是另一条规则，本次不改。inode 仍是 118774381。测试前目录 mtime 为 1794043814064519494；测试后变为 1794044254030951926，之后仍会变。顶层条目数保持 80，测试前排序的前 40 个名字都还在。没有 `cardex.db`、`cardex.db-wal`、`cardex.db-shm`、`serve-addr`、`serve-loop.log` 或 `.doctor-write-*`。测试后唯一比旧 mtime 新的顶层条目是 46 字节的 `.lock`，下一次列出时已经消失。v2 只在 `store.Open` 里写 `cardex.db.lock`，而 v1 检查在打开数据库之前返回，所以只读的该测试只 `Stat` `cardex.db` 并 `ReadDir` `tasks/`，不会创建或删除 `.lock`。mtime 变化是正在运行的 v1 换锁。

## 修复记录
检查 8787 或 8788 是否被拒绝时不得进入阻塞 serve，须在 listen 前返回 → cmd/cardex/cli_test.go:791、cmd/cardex/home_takeover_test.go:205 → TestServeLoopsUntilStopped、TestServeAddrIsNotPortRefused
