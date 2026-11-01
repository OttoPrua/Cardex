# R3-D-F1

## 做了什么

lark 只拼 `lark-cli im +messages-send --chat-id --text`。提醒的投递时间、退避和 Hermes 身份写在本次 home 的 `notify/reminders.json`，新 deliverer 按注入时钟恢复。空 kind 用 dev 前言；没有模板的 kind（含 ranking）保留卡材料并标为待复核。replan 前言要求同步维护 `context.md`。问题 id 被剥离或截断时附上原 id 的稳定摘要，Hermes 任务身份和 inbox 文件名共用这条规则。

## 没做什么，以及原因

未做 ranking 模板（R4-C）。未做问题状态机，未把沉默当成同意。未发真实消息，未改 `cmd/cardex`。未改 design.md 或 goals.md。

## 接口请求

无。是否已回答由调用方决定，本包只恢复投递账本，不读其他包。

## 已知问题

Hermes 仍只在同一次投递里核对一次 ack，不等待。

## 修复记录

N1 可选适配器命令必须符合实际 CLI，测试核对完整 argv → internal/notify/deliver.go:227 → TestLarkArgvIsNotAProcess, TestDeliverOrderInboxWhenOriginFails, TestPartialFailureKeepsDedupeAndBackoff
N2 重启不丢失同一问题的最近投递时间、退避和 Hermes 身份 → internal/notify/deliver.go:77,109 internal/notify/reminder.go:44 → TestReminderStateSurvivesNewDeliverer
N3 空 kind 补默认前言，未知或缺模板的 kind 保留材料并标待复核 → internal/prompt/preface.go:17 internal/prompt/build.go:34 → TestBuildSoftKind
N4 重规划前言要求同步维护 context.md → internal/prompt/templates/replan.md:11 → TestPrefacesRender
N6 问题 id 转换或截断保留原 id 摘要，不同问题的投递身份不合并 → internal/notify/hermes.go:98 internal/notify/deliver.go:230 → TestQuestionIdentityStaysDistinct
