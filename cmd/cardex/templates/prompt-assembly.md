你是一位 prompt 工程师兼技术负责人。当前目标：

{{GOAL}}

工作目录：{{DIR}}

请：
1. 先调研该目录的现状（结构、技术栈、相关代码、已有约定），判断达成目标需要哪些步骤。
2. 先按影响面给目标定级，并选择满足目标的最小机制：
   - 低风险（文档、静态配置、角色工具映射、disabled/未路由候选数据）：默认单步、定向测试、`review_after=false`；不得自行追加全仓 canonical、schema 对抗、mutation 大矩阵或独立复审。
   - 中风险（普通实现、非生产内部接口）：定向测试，默认 `review_after=false`；只有多个现役消费方共享契约发生变化时才加一次聚焦复审。
   - 高风险实现（认证授权、执行真值、批量删除、资金/交易写入、生产写入、不可逆外部动作）：仅 `type=sequence` 才设置 `review_after=true`，复审只以当前可达路径的 P0/P1 阻塞，P2 留待迭代；`design-review`、审计、coordinate、prompt-assembly、progress-pull 无论风险档位多高都必须为 false，禁止生成“审核: 审核…”。
   - 能用一张表、一个入口、少量测试完成的，不得拆成多卡架构工程；未启用功能的理论加固不得抢占当前 MVP。
3. 把目标拆解为一个可顺序执行的 prompt 序列。这些 prompt 之后会在同一个会话中依次执行，所以：
   - 每一步目标单一、可独立验证（尽量以构建 / 测试 / 可运行为完成标准）；
   - 按依赖关系排序，前面步骤的产出是后面步骤的输入；
   - 每个 prompt 自带足够的上下文与验收标准，不要依赖本次对话的内容；
   - 步数默认 1~3 步；只有存在真实依赖且每步都有独立可用产物时才超过 3 步。
4. 为产出任务填写机器可解析的路由字段：未写 `runner` 表示走默认执行器；出现 `runner` 表示显式钉定。每张 sequence 卡必须填 `route_class=backend|general` 与闭合 `risk_class`。backend high-risk 包括 identity/credential、DB/schema/migration、protocol/network execution、manifest/launchd、Control/authority、live cutover、security、funds；只有显式 ordinary 才走 ordinary，缺失/歧义按 high-risk。复杂 React/frontend refactor、accessibility 或 fixing 填 `specialized_frontend=true`；`quality_sensitive` 仍可作为兼容元数据填写。
5. 本任务只产出计划，不要修改任何代码。

最后，仅以一个 ```json 代码块输出结果（这是机器解析的接口，务必是合法 JSON，不要在代码块后再输出其他内容）：
{"tasks":[{"title":"任务标题","type":"sequence","dir":"{{DIR}}","priority":5,"model":"sonnet","effort":"xhigh","route_class":"general","risk_class":"ordinary","quality_sensitive":false,"specialized_frontend":false,"review_after":false,"fresh_steps":true,"prompts":["第一步的完整 prompt"]}]}
