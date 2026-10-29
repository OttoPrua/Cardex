# Cardex v2 lane 通用规则

每个 lane 的 Goal 开始前先读本文件和 `docs/v2/design.md`。它们在 R1 之后位于每个 lane worktree 的 `docs/v2/` 下。

## 位置

- 你只在本 lane 的 worktree 里工作，路径和分支见 lane prompt。
- 集成分支是 `v2`，位于 `/Volumes/Storage/Development/Cardex/worktrees/cardex-v2/`。lane 不得向它合并或提交。
- v1 源码在 `/Volumes/Storage/Development/Cardex/repo/`，只读，作为素材来源。可以移植其中的逻辑，例如 CLI 参数、PTY 实现、错误文本分类，但不要沿用它的结构。

## 不碰运行环境

- 不读写 `~/.cardex`。唯一例外是 lane prompt 明确允许的只读读取。
- 不动 launchd，不动已安装的 cardex，不占用 8787、8788 端口。
- 不启动真实的 provider CLI 会话。真实渠道只在 R5 由 prompt 明确授权时使用。
- 测试一律用 `t.TempDir()` 作 `CARDEX_HOME`，端口用 0 或随机端口。
- 手工冒烟用 `/Volumes/Storage/Development/Cardex/cache/cardex-v2-dev/home/`，端口用 8790。
- 不 push，不打 tag，不发 Release。

## 写域

- 只改 lane prompt 列出的路径。
- 如果需要改别处（包括共享接口），不要直接改。在 `docs/v2/lane-notes/<lane>.md` 的“接口请求”一节写明：要改什么、为什么、最小改法。复核时决定。
- 例外：可以新建本 lane 写域内的任意新文件。

## 依赖

- 只用 Go 标准库，加上 `modernc.org/sqlite`（R1 引入）。
- 需要其他依赖时，按接口请求处理，不要自行添加。

## 约束原则（来自设计第 13 节）

- 硬底线只有设计第 13 节列的几条。实现时不要加新的阻断条件。
- 遇到不规范的输入，一律按软约束处理：补默认值、宽松解析，处理不了就标为待复核。
- 如果确实认为需要一条新门禁，写进 lane note 并说明它防的是哪类真实失败，不要自行实现。

## 测试与完成

- 每个功能都要有单元测试。
- `internal/e2e/` 里有 R1 写好的场景测试，默认处于 skip 状态。本 lane 负责的场景要取消 skip，并让它们通过。
- 完成的标准：
  - `go build ./...`、`go vet ./...`、`go test ./...` 全部通过；
  - 不得让其他 lane 的场景测试从 skip 变成失败；
  - lane prompt 的完成定义全部满足。
- 不做 lane prompt 以外的功能、优化、重命名或顺手清理。

## 交付

- 在本 lane 分支上提交，提交信息以 lane 编号开头，例如 `R2-C: ...`。
- 写 `docs/v2/lane-notes/<lane>.md`，不超过 40 行，包含四节：做了什么；没做什么，以及原因；接口请求；已知问题。
- 预算快用完时，先提交当前可编译的进度，并在 lane note 里写明剩余工作。
