# Cardex v2

这是 Cardex 的第二版骨架。设计、lane 规则和目标写在 [docs/v2/](docs/v2/)。

数据模型和 SQLite 存储可以读写；其余包只提供接口，实现返回 `ErrNotImplemented`。fake 执行器按 JSON 脚本行动。
