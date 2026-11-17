# 新手上手与新建项目体验（设计稿）

状态：草案，v2.2.2 起分批落地。目标读者：第一次装 Cardex 的编程新手，以及部署 Cardex 的 Agent。

## 1. 目标

- 「傻瓜式」首跑：Cardex 先把推荐配置准备好（渠道、定位、项目），用户基本只需确认。
- 高级设置不隐藏：每一步都能用对应的 `cardex channel/positioning/project` 子命令细调。
- 只有一个订阅的用户也能用满 T1–T3：同一家模型按强弱分档，并给出角色建议。
- 任何「卡住」都要有可见原因和下一步命令，不能静默等待。

## 2. 云电脑实测发现（2026-11-18，Linux 全新安装）

| # | 问题 | 状态 |
|---|------|------|
| 1 | 没有 `positioning propose`，定位只能手写 config.json | v2.2.2 已修（77949f28） |
| 2 | 新装后没有任何已批准定位，卡永远 ready，无提示 | v2.2.2 已修：serve 自动生成「待确认」起步定位并打印批准命令 |
| 3 | 无候选时没有原因 | v2.2.2 已修：`route_no_candidate` 事件 + `route explain`，缺能力（如 web）会点名 |
| 4 | 每周排名卡需要 web，新装没有带 web 的模型就永远挂着 | v2.2.2 已修（5ef57779）：没有 web 不建卡，只给部署方发一次通知 |
| 5 | doctor / channel check 对没登录的 CLI 报 OK | v2.2.2 已修：见 §4 |
| 6 | 401 后的问题只写「no other channel is available」 | v2.2.2 已修：写明哪个渠道没登录和登录命令 |
| 7 | Linux 上 doctor 报 sandbox-exec 缺失，并把所有没装的 CLI 都列成问题 | v2.2.2 已修 |
| 8 | 发布包含 AppleDouble `._` 文件，缺 docs/ 和 skills/ | 待做（P2） |
| 9 | `--version` 不识别，`--help` 打印两次 | 待做（P2） |
| 10 | 只有英文输出 | 待做（P2） |
| 11 | 问题只投递到文件收件箱 | 待做（P2），见 §7 |

## 3. 推荐首跑流程：`cardex setup`（待实现）

一条命令串起现在分散的步骤，每步默认值都已填好，用户回车即可：

1. **扫描 CLI**：在 PATH 上找 grok、codex、claude、cursor-agent、kimi、agy、opencode。
2. **查登录**：用不花额度的状态命令（§4）。没登录的给出登录命令，等用户登完再继续。
3. **冒烟**：对每个已登录的 CLI 跑一次极小的真实调用（单次上限很低），把失败分成四类给出不同建议：
   未登录（401/403）、额度用完（429/quota）、沙箱拒绝、网络不通。
4. **生成渠道**：按 §6 的默认并发和模型写入 channels。
5. **推荐定位**：按检测到的订阅组合生成定位提案（§5），展示成一张表，用户确认即 `positioning approve`。
6. **建项目**：问仓库路径、项目类型（应用 / 库 / 脚本 / 文档），按类型给默认验收命令和沙箱默认值（`project add --sandbox`）。
7. **检查 origin**：没有 git remote 时说明影响（不能开 PR），让用户选「仅本地」或补 remote。
8. **起服务**：启动 serve，打印 `cardex board` 的位置。

`--yes` 全部接受默认；`--advanced` 每步展开全部参数。

## 4. 登录检查（v2.2.2 已实现）

- `cardex doctor`：不执行任何 PATH 上的程序（已有测试约束）。只检查已配置渠道用到的 CLI 是否存在、grok 是否有 `~/.grok/auth.json`、哪些渠道被标为 `auth_failed`、定位是否已批准。没配渠道时列出 PATH 上找到的 CLI。
- `cardex channel check`：额外运行各 CLI 自己的状态命令：

| CLI | 命令 | 判定 |
|-----|------|------|
| codex | `codex login status` | 退出码 0 |
| cursor-agent | `cursor-agent status` | 退出码 0 且含 "Logged in as" |
| claude | `claude auth status` | JSON `loggedIn: true` |
| grok | 无状态命令（`grok usage` 登录与否都失败） | 看 `~/.grok/auth.json` 是否存在且非空 |
| kimi / agy / opencode | 暂无可靠检查 | 不报；首跑 401 后由 §2-6 的问题提示 |

输出只用退出码和关键字，不打印 CLI 原文（里面可能有账号邮箱）。

## 5. 定位推荐与单订阅分档

原则：

- T1 放最强、最贵的模型，承担设计、复核、难 bug；T2 日常开发主力；T3 便宜快的模型，做机械改动、文档、测试补齐。
- 复核卡优先换一个模型族，同族只作兜底。
- 推荐表必须来自公开评测（编程类基准、官方模型卡），每次 ranking 卡更新；没有联网能力时不自动改表，只提示（§2-4）。

单订阅分档（建议，按当前 Otto 环境的模型名举例）：

| 只有 | T1 | T2 | T3 |
|------|----|----|----|
| ChatGPT / Codex | gpt-6.2-astra | gpt-6.2-sol | gpt-6.1-sol |
| SuperGrok | grok-4.7（goal_mode、web） | grok-4.7 | grok 的快速型号 |
| Kimi Code | kimi 最强型号 | 同左 | 同左（仅一个型号时三档共用，靠并发和红线控制花费） |

已知限制：现在 `LaunchSpec` 没有推理强度字段，同一个模型无法按 low/medium/high 拆成不同档。
要做到「同一模型、不同思考强度」分档，需要给 Positioning 加 `effort`，并让 codex/grok/agy 执行器传下去。列为 v2.3 候选。

## 6. 默认并发

grok 16、cursor 16、kimi 8、codex 8（2026-11-18 起）。setup 生成渠道时用这组默认，高级模式可改。

## 7. 部署方 Agent 的职责

Cardex 常由一个 Agent 帮用户装好。凡是「需要用户拿主意但不紧急」的事，先通知部署方 Agent，由它决定是否问用户：

- **每周模型排名**：没有带 web 的已批准模型时不建排名卡，向第一个活跃目标发一条一次性通知（scope `schedule:ranking`），让部署方三选一：
  1. 由部署方自己建定时任务做排名，结果用 `cardex positioning propose --file` 提交，并把 config 设为 `"schedules": []`；
  2. 给一个能联网的模型加 `web` 能力（propose 后 approve），之后排名卡自动出现；
  3. 直接关掉：`"schedules": []`。
  同时请它和用户确认检查频率，默认一周一次（168h），例如两周一次写 `"every": "336h"`。
- 问题投递：目前只落文件收件箱 `<home>/inbox/`。计划让 setup 记录部署方的回调（codex / hermes / lark 适配器），新装默认投给部署方。

## 8. 落地顺序

1. v2.2.2：§2 的 1–7（已完成，本地分支 `feat/v2.2.2-sandbox`，未推送）。
2. v2.2.3：`cardex setup`（§3）、发布包修正、`--version`/`--help`。
3. v2.3：定位 `effort` 分档、多语言输出、问题投递给部署方。
