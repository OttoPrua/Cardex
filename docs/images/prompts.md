# README 配图生成记录

使用 Codex 内置 imagegen 生成，2026-09-28。图片为流程示意；能力边界以 README 文字说明为准。

## cardex-overview.png

```text
Use case: infographic-diagram. Asset type: Chinese README editorial infographic for Cardex, a local AI coding task dispatcher.
Style: beautifully clean light-background technical editorial illustration, flat vector-like raster artwork, restrained indigo and teal accents, dark charcoal typography, generous whitespace. Small pale dot-matrix grids and tasteful monospace characters such as { } / > + as edge decoration, never competing with content. Crisp Chinese sans-serif text, large readable labels. Landscape 16:9. No photography, 3D, gradients, excessive shadows, imitation application screens, vendor logos or watermark.
Primary request: Explain cross-tool development management in ONE diagram.
Composition: top-left prominent "Cardex", below a large headline "一个对话，协调多个编程工具". Main diagram flows left to right with four clear columns: a small goal icon and "开发目标"; a conversation icon and "管理对话" plus smaller "Codex / Hermes"; a larger central stack of task cards labeled "Cardex" with subtitle "本地任务调度"; a right-hand vertical fan-out of three equal execution cards labeled "Claude Code", "Codex", "Grok" under heading "执行工具". Forward connectors from goal to management to Cardex to execution tools. A single thin return line underneath connects execution tools back to management, labeled "结果回传 · 继续推进". Below Cardex place a tiny local folder symbol labeled "任务 · 状态 · 日志".
Footer text exactly "管理 Agent 需能访问 Cardex 所在机器；各工具使用各自订阅".
Do not imply that subscriptions become management-agent native credits. Render only specified text. Keep all body text large and readable at typical README width. Code glyph decoration may form an abstract dotted terminal cursor near the top right.
```

## cardex-work-modes.png

根据 owner 反馈编辑原图：设计卡分支到多个执行卡再汇总；目标模式细分两种可并行方法。使用内置 imagegen；编辑参考为上一版工作方式图。

```text
Use case: infographic-diagram. Edit supplied Cardex three-mode infographic. Keep white background, navy typography, pale blue dot matrices and code characters, three-column layout and title.
LEFT unchanged: single explicit task to result.
CENTER column "02 分阶段编排": exactly ONE top "设计卡", THREE side-by-side "执行 A", "执行 B", "执行 C", ONE bottom "验收卡". Design branches to all three executions. All three execution cards MUST have visible vertical outgoing connector stems, including B in the center, joining one horizontal collector, then arrow to acceptance. Place the label "并行执行" beside the collector, NEVER covering center stem. Bottom caption "一次编排，多卡协作".
RIGHT column "03 目标模式": replace entire content under heading with a goal document icon and label "同一目标", then two clearly separated alternative method cards with NO CONNECTORS OR ARROWS between them or from icon. First card title "分阶段循环派遣", subtitle "设计、并行执行、验收，多轮推进". Second card title "模型原生 Goal 派遣", subtitle "每个会话一个方向，多个会话可并行". These cards are options, not steps. Use two clean flat cards with small teal and indigo squares. Bottom caption "两种方法，均可并行".
Main subtitle "从明确的一件事，到持续推进一个目标".
Footer exactly "工作方式示意；原生 Goal 支持范围取决于执行工具与平台".
Large readable Chinese labels. Do not use the terms 单会话目标执行 or 一个模型持续开发. Maintain the same spacious look and no fake UI.
```

## cardex-local-progress.png

```text
Use case: infographic-diagram.
Asset type: Chinese Cardex README illustration explaining saved local progress.
Primary request: Show how files preserve useful progress when a conversation pauses, without promising lossless recovery.
Landscape 16:9, clean white background, crisp dark navy Chinese sans-serif typography, restrained indigo and teal, generous whitespace, small pale dot-matrix grids and monospace { } / > + as corner decoration. Flat vector-like raster illustration with simple folder/document symbols, no photo, no fake application screenshot, no watermark.
Headline exactly "对话可以暂停，进度留在本地".
Main composition three columns with connecting arrows. Left: a conversation bubble with a pause symbol, heading "对话暂停", smaller label "限额 / 临时中断". Middle: a prominent open local folder headed "本地保存". Four neat separate document tiles around or inside folder labeled exactly "任务卡" "状态与日志" "代码与产物" "结果记录". Folder grounded on a thin horizontal baseline, a subtle dot matrix under it. Right: heading "接着推进", with three large clearly readable numbered lines vertically connected downward: "1 读取已有进度" then "2 核实执行状态" then "3 续跑或重建上下文". An arrow from left bubble toward central folder suggests progress stays available, and an arrow from folder to right flow suggests reading persisted files. No green success badge or automatic magic restart.
Footer exactly "保存的是任务与工作文件；恢复能力取决于执行工具和实际状态".
All important labels large. No extra text other than tiny decorative code glyphs. Make it approachable and visually calm, in the same editorial style as a clean developer-tools README.
```


## cardex-goal-methods.png

使用内置 imagegen 新生成。

```text
Use case: infographic-diagram.
Asset: Cardex Chinese README, goal-mode detail illustration. Brand style: clean white, dark navy crisp large Chinese sans-serif typography, indigo and teal accents, thin neat connectors, small pale dot matrix and decorative code braces in corners. Landscape 16:9, generous whitespace, flat vector-like raster artwork. No fake screenshot, vendor logos, watermark.
Main title "目标模式：两种派遣方法". Subtitle "两种方法都可以并行，区别在于如何持续推进".
Two equal panels separated by thin pale vertical rule.
LEFT title "分阶段循环派遣". Subtitle "管理层组织每一轮". Diagram vertically:
One top rounded box "整体目标".
Arrow to one large lightly outlined container titled "一轮编排". Within this container, one top box "设计卡", arrows branching to two SIDE BY SIDE boxes "执行 A" and "执行 B", visibly merge their two outgoing connectors to one bottom box "验收卡". All arrows point down. Outside container underneath label "下一轮". Arrow from acceptance down to next-round label; a thin loop from next-round label goes up OUTSIDE the container left edge back to DESIGN CARD, clearly labeled "未完成则继续". Do not loop back to the whole goal as if starting from scratch.
RIGHT title "模型原生 Goal 派遣". Subtitle "各会话持续推进各自方向". Diagram one top rounded box "整体目标", branching into THREE tall parallel equal containers titled "会话 A" "会话 B" "会话 C". Each container has a small monospace "goal" command label and one bounded vertical flow "开发" then "测试" then "修复", with tiny return arrow from repair to development entirely inside that conversation container. Different conversations have no cross-arrows. Under the three containers, all three outgoing stems merge to one bottom box "汇总结果". This represents multiple independent goal sessions in parallel, each continues its own direction.
Footer "并行前明确依赖与写入范围；原生 Goal 需执行工具支持".
Use only specified text and tiny corner code decorations. Everything fits without overlap; main labels legible at README width.
```

## 名称与整体验收修订

使用内置 imagegen 编辑，参考为各自上一版图片；以下提示词为当前最终修订。

### cardex-work-modes.png

```text
Edit the supplied Cardex infographic with minimal changes. Preserve all composition, icons, fan-out/fan-in arrows, typography, white background and pale dot-matrix decoration. Change the second column heading from "02 分阶段编排" to EXACTLY "02 单阶段编排". Change third column heading from "03 目标模式" to EXACTLY "03 自持开发". Keep first column "01 单卡直派". All other content unchanged except bottom of right column: replace "两种方法，均可并行" with "两种方法，均可并行". Do not change the task graphs or introduce any new text. Crisp Chinese text and same landscape proportions.
```

### cardex-goal-methods.png

```text
Edit supplied Cardex two-method infographic. Keep white background, navy/teal/indigo palette, dot matrix/code decoration, two-panel layout, left staged loop, right three independent Goal conversations with each development/test/repair loop.
Change headline from "目标模式：两种派遣方法" to EXACTLY "自持开发：两种派遣方法".
RIGHT panel final convergence must represent ALL THREE Goal conversations finishing first, THEN one overall acceptance round arranged by the management Agent. Replace the bottom box "汇总结果" with a larger box labeled "整体验收", with smaller second line "管理 Agent 安排". On the collector line above this box add readable small label "各方向开发完成后". Preserve three distinct outgoing stems converging, then ONE downward arrow into this one acceptance box. Do not add separate review boxes to individual conversations. No arrow bypasses overall acceptance.
Footer replace with "原生 Goal 需工具支持；整体验收由管理 Agent 显式安排，尚非自动触发".
Left panel entirely unchanged including stage-loop acceptance. Right title "模型原生 Goal 派遣" unchanged. Subtitle "两种方法都可以并行，区别在于如何持续推进" unchanged. Keep everything readable and no overlaps, enlarge bottom area as needed within same proportions. This is a managed workflow illustration, not a claim of automatic whole-goal acceptance.
```

## 最终 README：复杂度与验收时机

cardex-work-modes.png 使用内置 imagegen 编辑；其余三张采用此前已确认版本。README 完整长图由实际 Markdown 渲染导出，不由模型重绘正文。

```text
Use case: infographic-diagram. Edit the supplied final Cardex work-modes illustration with only these targeted wording refinements, preserving all existing graphics, layout, fan-out/fan-in arrows, dot-matrix and code-character decorations, navy typography and white background.
Main title stays "三种工作方式".
Replace subtitle with EXACTLY "先判断任务复杂度，优先跑通最小可用结果".
Keep headings exactly "01 单卡直派", "02 单阶段编排", "03 自持开发".
LEFT keep single task leading to checkmark. Change the bottom caption to "简单任务，一张卡完成并自检".
CENTER keep one design card → three parallel execution cards A/B/C → one acceptance card, all incoming and outgoing connectors. Change bottom caption to "本轮执行完成后，统一验收".
RIGHT keep the same goal icon and two separate method cards, titled "分阶段循环派遣" and "模型原生 Goal 派遣". First method subtitle becomes "每轮验收，再推进下一轮". Second method subtitle becomes "多会话可并行，完成后整体验收". Bottom caption stays "两种方法，均可并行".
Footer replace with "管理 Agent 按需编排；不默认增加多模型或额外审核".
Ensure Chinese labels are perfectly readable and no overlap, preserve airy white space. This diagram explains recommended managed working practices, not autonomous scheduler features. No new icons or boxes.
```

## English README illustrations

Generated with built-in imagegen from the matching Chinese reference images. Long explanations remain editable Markdown blockquotes in README.en.md.

### cardex-overview.en.png

```text
Use case: text-localization. Create the English edition of the supplied Cardex overview diagram. Preserve the same clean white background, navy typography, teal/indigo accents, dot-matrix and code-character decorations and all flow arrows. Use crisp English sans-serif typography with generous whitespace. Keep "Cardex" branding.
Replace headline with "One conversation. Multiple coding tools."
Map the four main columns: "开发目标" -> "Your goal"; "管理对话" -> "Manager session"; retain "Codex / Hermes"; central "Cardex" stays, subtitle "Local task scheduling"; right heading "Execution tools" with "Claude Code", "Codex", "Grok" unchanged.
Inside the central stack replace the three Chinese rows with "Task A", "Task B", "Task C" (these are task records, not an autonomous planning engine).
Folder label "Tasks · State · Logs". Return-arrow label "Results back to the manager".
Remove the long explanatory footer completely; this information will be real text in the README, not baked into the image. Preserve tiny decorative code glyphs. No Chinese text remains anywhere. Do not add extra claims or fake UI. Save a clean landscape English companion illustration.
```

### cardex-work-modes.en.png

```text
Use case: text-localization. Make an English edition of this Cardex three-mode diagram, preserving white/navy/teal/indigo style, light dot matrices and code glyphs, all layout and topology.
Title: "Three ways to work". Remove the explanatory subtitle and long footer; explanations will be editable README text.
Three headings, wrap cleanly onto two lines as needed: "01 Single-card dispatch", "02 Single-stage workflow", "03 Sustained development".
LEFT labels: task "Defined task", checkmark "Build & check". Remove the long bottom caption.
CENTER graph exact one "Design" card at top → three parallel cards "Task A", "Task B", "Task C" → one "Acceptance" card below. Keep all three outgoing stems visibly merging, including the middle one. Label beside collector "Parallel work". Remove long bottom caption.
RIGHT document label "Shared goal". Two separate method cards, NO sequential arrow between them: "Staged cycles" with subtitle "Accept each round, then continue"; "Native Goal sessions" with subtitle "Parallel sessions, overall acceptance". Bottom short caption "Both methods can run in parallel".
No Chinese text anywhere. Keep generous whitespace and all text crisp, readable and unclipped. Avoid excessive tiny labels; no additional claims.
```

### cardex-goal-methods.en.png

```text
Use case: text-localization. Produce English edition of the supplied Cardex sustained-development comparison diagram, preserve exact graph topology, white background, navy/indigo/teal palette and light dot-matrix/code-glyph decorations.
Main heading "Sustained development".
Remove long explanatory subtitle and footer; explanation will be native README text.
Left panel title "Staged cycles". Small subtitle "Manager coordinates each round".
Left diagram label translations: "整体目标" -> "Shared goal"; container label "一轮编排" -> "One round"; "设计卡" -> "Design"; "执行 A" -> "Task A"; "执行 B" -> "Task B"; "验收卡" -> "Round acceptance"; "下一轮" -> "Next round"; external return-loop label "未完成则继续" -> "If work remains". Preserve one Design branching to two tasks, both merging into round acceptance, then next round looping back to design. Readable short labels, no overlap.
Right panel title "Native Goal sessions". Small subtitle "Each session advances its direction".
Top "Shared goal", three parallel containers "Session A", "Session B", "Session C"; each contains "goal", then "Build" → "Test" → "Fix" with within-session return loop. Keep separate outgoing stems from ALL THREE sessions converging to one final acceptance.
Collector label "All required directions complete".
Bottom box two lines "Overall acceptance" and smaller "Arranged by the manager".
No Chinese remains. No per-session acceptance boxes. Preserve a single overall acceptance box. Make clear this is manager-arranged acceptance, not automatic. Use spacious typography, wrap long labels cleanly if required, no clipping.
```

### cardex-local-progress.en.png

```text
Use case: text-localization. Create English edition of supplied Cardex local progress infographic. Preserve the clean white background, navy text, blue folder, teal/indigo accents, dot matrices and code glyph decorations. Keep same three-column composition: paused conversation → saved files → steps to continue.
Headline "Conversations pause. Progress stays local."
LEFT heading "Conversation paused", short subtitle "Quota limit / interruption". Keep pause-bubble illustration.
CENTER heading "Saved locally". Four folder documents labeled "Task cards", "State & logs", "Code & artifacts", "Result records".
RIGHT heading "Continue the work". Three numbered steps exactly "1 Read saved progress", "2 Verify execution state", "3 Resume or rebuild context". Keep numbering in circles and step labels in roomy boxes, don't duplicate numbers within text.
Remove explanatory footer; it is provided as editable text in README.
No Chinese text remains. Fit and wrap English labels naturally without overlap or clipping. Do not imply guaranteed recovery or saved model-internal state. Match visual style of the original, spacious, readable English sans-serif typography.
```
