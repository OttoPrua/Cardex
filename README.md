# Cardex

**在一个管理对话里管理目标，让不同的 AI 编程工具协作交付。**

**中文** · [English](README.en.md) · [安装与新手引导](#安装与新手引导) · [支持的工具](#支持的工具与接入方式) · [下载源码](https://github.com/OttoPrua/Cardex/releases/tag/v2.0.0)

![Cardex：Codex 或 Hermes 管理会话交给本机 Cardex 编排，再派发给已配置的执行工具。](docs/images/cardex-v2-overview.svg)

Cardex 是运行在你机器上的开发编排工具。你在 **Codex 或 Hermes 管理会话**里定目标、答关键问题；Cardex 拆分和调度工作，执行工具使用各自的订阅或 API。目标、卡片、尝试和决策存在本地 SQLite，代码留在 Git，执行和验收日志也留在本机。关掉管理对话，已保存的工作还在。

## 安装与新手引导

**一键引导（推荐）：把这段发给能操作这台电脑的 Codex 或 Hermes 管理会话。** 云端会话先连接 Cardex 主机。已下载源码的话，告诉 Agent 解压后的目录，就可以从那一步继续。

```text
请在这个对话里帮我安装并配置 Cardex。若我已下载源码，从解压后包含 go.mod 的目录继续；
否则从 https://github.com/OttoPrua/Cardex/releases/tag/v2.0.0 取得源码。
阅读 docs/v2/guide.md 和 skills/cardex-dispatch/SKILL.md，并在同一对话逐步引导：
1. 说明当前管理会话和运行主机，和我确认数据目录。先从源码构建，再运行 doctor。
2. 告诉我检测到哪些执行 CLI，问我想接入哪些已有订阅或 API。帮我在对应工具内完成认证，
   区分「已安装」「已登录」与「有权使用的模型」。无需把密钥发到对话里。
3. 按我的账户、任务和额度推荐 T1–T3 模型、复核模型族和额度红线，
   说明为什么这样配；让我选择采用或调整，确认后才保存，重新运行 doctor。
4. 推荐手动目标或自动目标，展示第一张卡及验收方式。
   先让我确认，再启动调度或执行任务。
```

**第一次下载后手动开始**（macOS shell，需 Git 和 Go 1.24+）：

1. 在 [下载页](https://github.com/OttoPrua/Cardex/releases/tag/v2.0.0) 下载 **Source code (zip)** 或 **tar.gz**，解压并在终端进入包含 `go.mod` 的目录。
2. 粘贴这一条命令，在当前目录构建程序，并检查用于试用的独立数据目录：

```sh
go build -o ./cardex ./cmd/cardex && ./cardex --home "$HOME/.cardex-demo" doctor
```

3. 命令跑完后就可以开始配置。首次检查提示缺少渠道或模型定位是正常的；回到同一管理会话继续引导，或按下方的 [第一个目标](#跑通第一个目标) 配置。

发行页提供源码包。安装示例统一使用 `~/.cardex-demo`，首次试用可以沿用这个目录；也可以通过 `--home` 或 `CARDEX_HOME` 选择其他位置。执行器的系统级写入隔离目前由 macOS 实现。

[使用指南](docs/v2/guide.md) · [管理会话的派发 Skill](skills/cardex-dispatch/SKILL.md)

## 支持的工具与接入方式

| 执行工具 | runner | 如何接入 |
| --- | --- | --- |
| Claude Code | `claude` | 复用 CLI 登录，或通过该 CLI 接入 Anthropic Messages 兼容服务 |
| Codex CLI | `codex` | 复用已登录的账户，或使用 CLI 配置的 Responses 兼容 provider |
| Grok Build | `grok` | 使用 Grok CLI 的账户及实际可用的模型 |
| Cursor Agent | `cursor` | 使用 Cursor 账户有权使用的模型 |
| OpenCode | `opencode` | 使用已配置的 `provider/model`，例如 OpenCode Go 或自定义兼容服务 |
| Kimi CLI | `kimi` | 复用 Kimi CLI 的账户与模型配置 |
| Antigravity | `agy` | 使用 Antigravity CLI 与其可用模型 |

**订阅、按量 API 和兼容网关都通过对应的执行工具接入。** 例如 Anthropic Messages 走 Claude Code，OpenAI Responses 可走 Codex CLI，OpenAI Chat Completions 可通过 OpenCode 配置对应 provider。Cardex 传递已配置的模型 ID，不做协议转换；模型、思考强度和额度取决于实际账户与 CLI。OpenCode 是工具，OpenCode Go 是服务方案。

源码还保留 [Claude 兼容引擎档案](internal/runner/engines.go)：Kimi Code、GLM、MiniMax、MiMo、OpenCode Go、Ollama Cloud 等。档案在 Claude 渠道绑定 `engine_profile` 时使用；当前 `channel set` 不提供该绑定参数，新装用户可先在执行 CLI 中配置服务。

## Cardex 能帮你做什么

- **用一个对话管理开发。** 小改动可以只加一张卡；跨模块的目标可以让 Cardex 做仓库盘点、设计和拆卡，你只需处理需要拍板的问题。
- **给每张卡找合适的模型。** 按任务档位、模型能力、渠道健康、并发和估算额度选路。用 `route explain` 查看一次派发为什么选这个模型。
- **让独立工作并行，验证后再合入。** 每次尝试使用独立 Git worktree，写入冲突的卡等待。开发卡由不同模型族复核，项目测试通过后才推进目标的集成分支。
- **对话暂停后还能接着做。** 卡片、结果、尝试日志和代码留在本地。新的尝试可以从卡分支继续，带上已保存的上下文和复核意见。

**遇到额度或认证问题，符合条件时可重新选路。** 新尝试从已保存的卡分支接着做；没有合适的渠道时会等待或提出问题，执行器明确拒绝时不会静默换工具。

![自动目标的流程：设计方向、使用者确认、独立执行卡并行、复核与测试、合入目标分支。](docs/images/cardex-v2-objective.svg)

> **小任务用手动目标，多步骤交付用自动目标。** `manual` 只处理你添加的卡，不自动生成设计和拆卡；`auto` 会推进设计树和依赖。默认 `safe` 模式由你确认方向和模块冻结；`fast` 可自动冻结已就绪的模块和细节，方向仍须确认。目标集成分支和项目主分支是两个交付步骤，推送和发布另行决定。

## 跑通第一个目标

先在执行 CLI 中完成登录或 API 配置。以下用 Codex 开发、Claude 复核作示例；也可换成你已配置的不同模型族。替换仓库路径、模型 ID 和测试命令：

```sh
export CARDEX_HOME="$HOME/.cardex-demo"
./cardex project add --id demo --repo /path/to/repo --test-cmd 'go test ./...'
./cardex channel set --id my-codex --runner codex --model 'YOUR_AUTHOR_MODEL_ID' --concurrency 1
./cardex channel set --id my-claude --runner claude --model 'YOUR_REVIEW_MODEL_ID' --concurrency 1
./cardex channel check
./cardex --json positioning show
```

没有已批准的定位表时，可在数据目录的 `config.json` 中填入经你确认的初始 `positioning`（模型 ID、模型族、档位与能力）；后续通过 `positioning proposals` 和 `positioning approve ID` 审阅更新。给渠道设定与你账户匹配的额度窗口和红线，再运行 `doctor`。例如，T1 可做事实核对，T2 可做常规开发，T3 可做设计与复核。按实际能力分配，复核要有不同模型族的可用渠道。

<details>
<summary>初始模型定位示例</summary>

把示例合并到数据目录中的 `config.json`，保留原有配置。模型 ID 须与渠道登记一致；换模型时也要核对模型族和档位。

```json
{
  "positioning": [
    {
      "id": "starter-dev",
      "model": "YOUR_AUTHOR_MODEL_ID",
      "family": "gpt",
      "tiers": ["T1", "T2"],
      "capabilities": [],
      "preference": 1
    },
    {
      "id": "starter-review",
      "model": "YOUR_REVIEW_MODEL_ID",
      "family": "claude",
      "tiers": ["T3"],
      "capabilities": [],
      "preference": 1
    }
  ]
}
```

</details>

配置就绪后，可先让 Cardex 管一项有验收标准的工作。用真实管理会话标识替换 `YOUR_THREAD_ID`；Hermes 使用 `hermes:PROFILE/SESSION`。

```sh
./cardex objective add --id first-change --project demo --mode manual \
  --title '修正导出说明' --origin 'codex:YOUR_THREAD_ID'
./cardex card add --objective first-change --kind dev --tier T2 \
  --title '补齐导出示例' --body '更新导出文档，保留现有行为，并运行项目测试'
./cardex serve --addr 127.0.0.1:0
```

`serve` 同时开启调度和只读看板；准备好运行待派发的卡后再启动。打开它打印的地址，可看目标、卡、尝试、渠道和待确认事项。管理会话用 `--origin` / `CARDEX_ORIGIN` 关联任务，按 [通知配置](docs/v2/guide.md#配置通知) 登记接收者。要核对一次派发，读 `card show ID` 和 `route explain ID`；要查看待选答案，读 `--json question show ID`。

> 看板只读但**没有鉴权**，会显示目标、卡片和问题内容。需要远程查看时，绑定本机或 Tailscale 这类私有网络地址，不要用 `0.0.0.0` 直接暴露到公网。

[设计说明](docs/v2/design.md) · [PolyForm Noncommercial 1.0.0](LICENSE)
