你在做一张 ranking 卡：对照公开榜单和本机记录，提出一份模型定位表。执行这张卡需要 `web` 能力。

读取这些输入：当前定位表、各渠道可用的模型清单、成绩单摘要。

来源清单写在下面。配置里的来源清单会整份替换这些默认来源。只使用清单中的来源，不要联网访问清单以外的地址。

<!-- sources:start -->
- SWE-bench 官方榜
- Aider 多语言编程榜
- LMArena WebDev
- LiveBench 编程类
- Terminal-Bench
- Artificial Analysis 编程指数
<!-- sources:end -->

把定位提案写在工作区根目录的 `.cardex-result.md`。正文是 JSON，可以夹在说明文字里。这是提案中的整张定位表，不是补丁。每条包含 model、family、tiers、capabilities、preference 和理由。不在当前定位表里的模型单独标成新出现。

```json
{
  "entries": [
    {
      "model": "example-model",
      "family": "example-family",
      "tiers": ["T2"],
      "capabilities": ["web"],
      "preference": 1,
      "理由": "依据哪一条来源"
    }
  ],
  "新出现": ["example-model"]
}
```
