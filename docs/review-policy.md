# 复核策略（review policy）

这份文档是 Cardex 复核规则的唯一出处。代码默认值、plan 卡提示词、`cardex-dispatch` skill 和管理会话都按这里执行。

## 一、三层开关

复核模式可以设在三层，按 **卡 → 目标 → 项目** 的顺序取第一个非空值。三层都空时按 `auto` 处理。

```
cardex project set   --id <p> --review check
cardex objective set --id <o> --review review
cardex card add ...  --review off
```

| 值 | 含义 |
| --- | --- |
| 空 / `inherit` | 沿用上一层 |
| `auto` | 由 Cardex 决定：开发类卡走 `review`，档位按改动风险定 |
| `off` | 不复核，验收通过后直接进合并队列，事件里记 `review_skipped` |
| `check` | 完成度检查：T3，只对照目标和交付物检查有没有做完，不审代码 |
| `review` | 正常复核：档位按改动风险定（见第三节） |

开发类卡指 `dev`、`skeleton`、`repair`、`rebase`、`backlog`。`research`、`spike` 等卡种本来就不合并，也不进复核。

## 二、按任务类型的推荐

| 任务 | 推荐 | 说明 |
| --- | --- | --- |
| 复杂设计（design 节点、架构方案） | 强模型出稿、强模型复核，推荐换厂商 | 由管理会话另建 T1 的 review 卡补充视角 |
| 普通开发 | `auto`（默认） | 小改动用 T3 查，高风险的用 T1 |
| 通用任务（文档、资料、截图、报告） | `check` | 只核对交付物是否齐全；缺了就补缺的部分 |
| 一次性的研究、实验 | `off` 或不设 | 卡种本身不合并 |

降低复核（改成 `off`，或者把 `review` 改成 `check`）需要先问使用者。提高复核可以直接改，事后告诉使用者。plan 卡只能写 `check` 或 `review`，写 `off` 会被忽略。

## 三、review 模式的档位

Cardex 读这次执行起点和终点之间的 `git diff --numstat`：

1. 读不到 diff：T1。
2. 改到高风险路径：T1。高风险路径包括 sandbox、permission、auth、secret、credential、token、password、migrat、schema、security、crypto、`.github/workflows/`、Dockerfile、`.plist`、launchd，以及项目的 `--forbidden` 路径。
3. 大改动（超过 600 行或 20 个文件）：T1。
4. 小改动（不超过 60 行且不超过 3 个文件）：T3。
5. 其余：和作者同档（卡上的 tier，没有就用卡种默认档）。

复核卡的事件 `review_planned` 记录模式、档位和原因。

## 四、换厂商是推荐，不是硬性要求

路由给复核卡选模型时：

1. 本档有别的模型族可用：只在别族里选，同族记为 `family` 过滤。
2. 本档只剩作者同族：往上借一档，那一档有别族就用别族。
3. 还是没有别族：用同族（换个模型，或者同一模型开新会话）。

很多使用者手上只有一两家的订阅，所以不强制换族。

## 五、复核结论

写在 `.cardex-result.md` 的 `## 复核结论`，第一个词是结论：

| 结论 | 处理 | 计返工 |
| --- | --- | --- |
| `pass` | 进合并队列 | 否 |
| `rework` / `missing` | 意见带回开发卡重做；`missing` 是 check 模式的写法，只补缺的部分 | 是 |
| `blocked` | 复核方无法核实（环境、权限、缺资料），开发卡转 needs_input，向使用者提问 | 否 |
| `escalate` | 复核方没把握，升一档再建一张复核卡；T1 还没把握就向使用者提问 | 否 |

review 模式的提示词借用 Codex 本地 review 的结构：只报这次改动引入的问题，按 P0–P3 标优先级，不提风格偏好；只有 P0、P1 才给 `rework`；结论后写置信度。能用脚本验证的项放进验收命令，不留给复核。


### 查看当前复核

`cardex card show <id> --review-progress` 只输出该卡的当前复核记录，可加 `--json`。普通 `card show --json` 和看板卡片也带有同一份投影；既有单行文本、`--brief` 和 `--summary` 不变。投影区分排队、暂停、等待、无路由、已启动和已吸收，并按实际尝试显示模型和复核提交。仅显示能归属于当前尝试的结论；归属冲突或缺少证据时不会猜测已通过。此信息不作为合并许可，也不改变调度或复核资格。
