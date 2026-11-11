# 从 Cardex V1 升级到 V2

本说明供负责安装和配置的 Agent 使用。只有确认机器上已有 V1 数据或服务时才走升级流程。先确认使用者希望独立试用还是切换既有安装；拿到源码、构建新版或预览导入，都不代表已获准停止服务或启动任务。

## 先读现状，预览导入

1. 确认当前二进制、V1 数据目录、调度器与在途任务；确认一个不同的 V2 目标目录。保留 V1 数据、二进制和服务配置，作为回退依据。下面的 `./cardex` 必须指向已构建的 V2 二进制。
2. 用真实路径替换变量，先跑只读预览。把将变为就绪、暂停或历史的卡片数量，以及未解析项，说明给使用者。

```sh
v1_home='/path/to/existing-v1-home'
v2_home='/path/to/separate-v2-home'
./cardex --home "$v2_home" import-legacy --dry-run "$v1_home"
```

导入只读取 V1 源目录。V1 的 `queued`、`running` 卡会在手动目标下变为 `ready`；这不是恢复原进程或旧模型会话。`held` 卡保留为暂停，已结束的卡进入只读历史。原有路由只是提示，渠道和模型定位要重新核对。预览不创建目标数据目录。

## 确认后导入和切换

1. 获得切换同意，确认原执行已结束，并停止对应的 V1 调度，避免新旧安装重复派发。V1 数据和回退材料继续保留。
2. 导入到确认的 V2 目录，核对项目、目标、卡片和配置，再继续同一管理对话：

```sh
./cardex --home "$v2_home" import-legacy "$v1_home"
./cardex --home "$v2_home" objective list
./cardex --home "$v2_home" card list
./cardex --home "$v2_home" doctor
```

3. 按[使用指南](guide.md)核对执行渠道、定位表和通知接收者。向使用者说明哪些已导入卡处于就绪状态；确认后再用 `serve` 启动调度和只读看板。不要为展示看板而提前启动包含就绪卡的目录。
4. 把管理会话和脚本改为 `--home` / `CARDEX_HOME`，用 `objective`、`card`、`channel` 等入口。V1 的 `-root`、`workflow`、`goal-run` 和独立 `cardex-board` 不适用于 V2。

需要回退时，按已记录的路径恢复 V1 的二进制、数据入口和服务配置，保留 V2 已产生的数据供核对。不要用 V2 数据库覆盖 V1。具体服务切换与回退顺序见[设计文档中的迁移方案](design.md#14-迁移与切换)。
