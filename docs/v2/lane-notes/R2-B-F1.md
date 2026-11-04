# R2-B-F1

## 做了什么
已有 GROK_HOME 同时用于 goal 启动环境与 WritablePaths，未设置时两者用 ~/.grok。Kimi 顶层权限键按等号切分并修剪空白。权限检查只拒绝旗标位置和 --permission-mode 的危险操作数。分类读取嵌套 error.message，用量解析同时接受完整 JSON 文档和 JSONL。updates.jsonl 的无关坏记录被跳过。Drive 在 0, io.EOF 时检查已缓存的额度尾行。v1 样本摘录放在 testdata。

## 没做什么，以及原因
未运行真实 CLI，未改 runner.go、fake.go、R2-A 适配器、model、go.mod、design.md、goals.md。未实现 PTY，未改 Kimi 隔离家目录，未声明 web。Kimi ParseUsage 仍为空：v1 不记录 token 字段。无头 stderr 与无头 error JSON 仍不是交互额度卡。PTY 上的 signal: killed 仍不是崩溃事实。

## 接口请求
无。

## 已知问题
无头 stderr 的 quota exceeded 与无头 HTTP 429 JSON 贴到 PTY 上仍不是 ErrGrokQuotaHit。goal/state.json 没有额度 status，ReadGrokGoalSession 不发明额度状态。

## 修复记录
N1 已有 GROK_HOME 同时决定 goal 启动环境和可写路径，未设置时用 ~/.grok → internal/runner/grok.go:77、grok.go:87、grok.go:153 → TestGrokHomeRespectsExisting
N2 分类和用量保留 v1 的嵌套 error.message 与完整 JSON 文档，JSONL 仍可解析 → internal/runner/grok.go:430、grok.go:824、grok.go:1358（cursor.go:55、kimi.go:121、agy.go:44、agy.go:75 经 classifyScanned 与 parseUsageAliases） → TestNestedErrorAndMultilineUsage
N3 无关坏记录不否决 updates.jsonl 里已有的有效终态 → internal/runner/grok.go:1275、grok.go:1290 → TestGoalSessionSkipsCorruptUpdates
N4 样本测试只读工作树内的 v1 摘录和来源说明 → internal/runner/grok_test.go:583、testdata/v1_sample_provenance.txt:1 → TestGrokQuotaAndCrashChannels
N5 顶层键与等号之间的空白（含制表符）仍是该键，旧赋值被替换 → internal/runner/kimi.go:91、kimi.go:109 → TestKimiPermissionKeyWhitespace
N7 EOF 结束此前缓存的无换行额度尾行，0 字节的 io.EOF 也检查 → internal/runner/grok.go:258 → TestDriveEOFChecksBufferedQuota
N8 交付 note 不超过 40 行，同类修复记录合并 → docs/v2/lane-notes/R2-B.md:38 → TestLaneNotesStayWithinLimit
参数值不是权限旗标；旗标位置和 --permission-mode 的危险操作数仍拒绝 → internal/runner/grok.go:174、cursor.go:42 → TestPrivilegeValuesAreNotFlags、TestPrivilegeFlagPositionStillRejects
