# Windows 原生与 WSL

Cardex 使用一条代码主线和一套产品版本号。构建产物按平台与架构区分，例如 `cardex_<version>_windows_amd64.zip` 和 `cardex_<version>_darwin_arm64.tar.gz`；发布、安装和运行验收分别进行，不维护 Windows/Mac 永久分支。相同版本号不代表所有平台能力均已验收。

## 原生 Windows（experimental）

Windows amd64 已用真实 Windows 上的受控本地进程验证：配置/状态读写、单实例锁与任务控制锁、路径/参数引号、Grok `.cmd` 与 Kimi `.exe` 适配器的结果持久化、启动拒绝、超时及拥有者崩溃清理。测试替身不访问真实模型；这些结果不代表真实账号、模型调用或交互式 Goal 已通过验收。

在 PowerShell 中构建并使用明确的数据目录：

```powershell
go build -o .\cardex.exe .
.\cardex.exe --version
.\cardex.exe init -root 'D:\Cardex Data'
.\cardex.exe list -root 'D:\Cardex Data'
```

配置中的原生路径使用 Windows 格式；JSON 中反斜杠写成 `\\`，或使用 `D:/Project/...`。已有安装升级时保留原二进制及配置，确认没有使用该路径的在途任务，再采用新构建；先用 `--version` 和 `list` 核对。`install-launchd` 是 macOS 专用命令。

Claude/Codex/Grok/Kimi 的 `.cmd`/`.bat` 入口通过系统 `cmd.exe` 调用。带空格、中文、尾部反斜杠及常见元字符的参数已有原生回归；批处理参数中的 `%`、`!`、双引号或换行会被拒绝，避免 shell 展开改变原意。这类参数需要原生 `.exe` 入口。Grok 提示词通过私有临时文件传递；Claude/Codex 提示词走标准输入。Codex 工作区配置使用保留反斜杠的 TOML 字面字符串；工作区含单引号时请使用原生 `.exe` 入口。

Windows 用按规范工作区路径命名的全局 Job 提供原子互斥，SSH 与桌面会话查询同一归属；只有该对象不存在时才视为没有本机 Cardex writer，访问失败仍保守阻塞。任务创建时从一个已纳入 Job 的短命 Cardex 辅助进程继承 Job；该辅助进程只等待管道 EOF，不调度任务或执行 provider。子进程在持久化归属前保持挂起。Job 在 Cardex 退出时清理其后代，辅助进程在创建窗口内失去拥有者时也会自行退出。已有父进程、不同 token 或 breakaway 启动设置会被拒绝。原生机制参考 [Microsoft ParentProcess / Job 继承说明](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-updateprocthreadattribute)。

无法证明没有遗留进程时不会放行后继。Windows 跨 runner 的自动 fallback 仍关闭；准入失败或启动后的归属/执行失败会挂起，已消耗尝试需明确 `retry` 才能建立新 epoch，不能靠重复调度清除。没有解除旧卡或共享 admission 暂停的隐含动作。

## WSL

WSL 使用 Linux 构建及 Unix 进程/锁实现。它的测试结果不等于 Windows 原生运行验收；Windows 的测试也不证明 WSL。两者使用各自的配置、认证和执行状态，同一任务选择一个执行面，避免双重派发。本轮未启动 WSL，也未验证 WSL 运行。

## 可复现的无模型回归

在 Windows 原生 Go 环境运行：

```powershell
go test . -run '^(TestWindows|TestExecutionLease|TestSetup|TestPreset|TestAcquireLockStealsAtomicallyNoDoubleOccupancy|TestDeniedPresemanticStartDoesNotConsumeResumeTombstone|TestReleaseLockRefusesForeignPID)' -count=1 -timeout=180s
go vet ./...
go build -trimpath -o bin/cardex.exe .
python scripts/smoke.py bin/cardex.exe
python scripts/test-install.py bin/cardex.exe
```

也可在 Mac 上用 `GOOS=windows GOARCH=amd64 go test -c -o cardex.test.exe .` 生成测试程序，再在 Windows 执行相同 `-test.run` 选择。交叉编译只是准备步骤；必须保留实际 Windows 运行结果。现有全套测试仍包含 POSIX shell fixtures，这个选择不宣称原生全套测试均通过。Mac 的 provider、admission、进程 lease 和 Goal 受影响回归独立运行。
