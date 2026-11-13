# 在 Cardex 卡里跑 Chrome 浏览器测试：沙箱需求报告

2026-11-13 SGT · 只调查，不改代码、不安装、不重启
实测机器：新 Mac（macOS 27.2.1，Google Chrome 157.0.8103.51）
对照的代码：Cardex main a733e93（v2.1.0），`internal/sandbox/profile.go`、`wrap_darwin.go`

## 1. 结论

**macOS 的 seatbelt 不能嵌套。** 一个进程已经在 `sandbox-exec` 里了，它和它的子进程就不能再调用 `sandbox_init` 给自己加一层沙箱，调用会返回 `Operation not permitted`。

Chrome 的渲染、GPU、网络、存储这些子进程启动时都要套上 Chrome 自己的 seatbelt profile。所以只要 Chrome 是在 Cardex 卡的沙箱里被拉起来的，它自己的沙箱就一定起不来，子进程逐个崩溃，Playwright 那边看到的就是 `target_crashed`。

这件事改 profile 改不好：哪怕外层只写一句 `(allow default)`、什么都不限制，照样失败（见第 2 节 open 那一行）。也就是说，缺的不是 mach-lookup、IPC、文件路径或网络中的哪一项，而是「外面已经有一层沙箱」这件事本身。

所以「Chrome 在 Cardex 沙箱里跑，并且 Chrome 自己的沙箱照开」这个组合在 macOS 上做不到。能做到的最小方案是：**让 Chrome 在 Cardex 沙箱外面启动，卡里的执行器从沙箱里通过 CDP（Chrome 的远程调试协议）去连它。** 第 3 节有实测，这条路是通的。

## 2. 实测

脚本就是第 5 节的验证方法。每一行都是一次 `--headless=new --dump-dom`，跑 25 秒后看 DOM 有没有吐出来。

| 场景 | 外层沙箱 | Chrome 自己的沙箱 | 结果 | 关键报错 |
|---|---|---|---|---|
| plain | 无 | 开 | ✅ 输出了 DOM | — |
| strict | 照 Cardex 的规则：读和网络放开，写只能写工作目录、TMPDIR、/tmp | 开 | ❌ 卡住，没有输出 | `Crashpad/settings.dat: Operation not permitted`、`Failed to create socket directory` |
| strict + `--no-sandbox` | 同上 | 关 | ❌ 没有输出 | 同上：写 `~/Library/Application Support/Google/Chrome/Crashpad` 被拒，ProcessSingleton 建不了 socket 目录 |
| open | 只有 `(allow default)` | 开 | ❌ GPU 进程退出，`GPU process isn't usable. Goodbye.` | `sandbox initialization failed: Operation not permitted` ×7、`aperitif: Failed to initialize sandbox` ×18、网络服务反复崩 |
| CDP 外连 | Chrome 在外面，不套沙箱 | 开（gpu、network、storage、utility 子进程都正常起来了） | ✅ 在 strict 沙箱里 `curl http://127.0.0.1:9333/json/version` 拿到了浏览器信息 | — |

对照已有的证据：

- **Cardex 卡里出现 `target_crashed`**（TestSSOBrowserTwoRPs）：和上面 open、strict 两行的表现一致。原因是嵌套沙箱，另外还有写 Crashpad 这类路径被拒。
- **旧 Mac 在 Cardex 外面用 Playwright 1.51 加系统 Chrome、`chromiumSandbox: true` 能跑**（`~/Projects/TShare-b1103-r5/artifacts/r5/evidence/chrome-launch.json`）：和 plain 一行一致，说明 Chrome 本身没问题，问题出在外层。我没去读这个文件，因为旧 Mac 上的 r5 还在跑。
- **NAS runner 容器里的 Chromium 报 `No usable sandbox`**：这是 Linux 上的另一回事，见第 6 节。

## 3. 方案：浏览器卡由 host 在沙箱外启动 Chrome

**范围：**只对标了浏览器测试的卡生效。建议用现有的能力标记 `card add --require browser`，不用新加字段，其他卡完全不受影响。

**流程**（都在 host 里，host 本来就在 seatbelt 外面）：

1. host 在包沙箱之前，用这张卡自己的用户数据目录启动 Chrome。
   - 用户数据目录是 `attempts/<id>/browser/profile`，Crashpad 的数据库也放在 attempt 目录下。
   - 启动参数是 `--headless=new --remote-debugging-port=0 --remote-debugging-address=127.0.0.1 --user-data-dir=...`。**不加 `--no-sandbox`。**
   - Chrome 用的是 host 自己的环境，不在执行器的 seatbelt 里，所以它自己的沙箱能正常初始化。
2. host 读 `<profile>/DevToolsActivePort` 拿到端口和 ws 路径，通过环境变量交给执行器：`CARDEX_BROWSER_CDP=ws://127.0.0.1:<port>/devtools/browser/<guid>`。
3. 执行器照常在 Cardex 沙箱里跑。测试代码用 `chromium.connectOverCDP(process.env.CARDEX_BROWSER_CDP)` 去连，不再自己 `launch()`。
   - Playwright 1.51 支持这种用法。
   - 现有 profile 本来就是 `(allow network*)`，从沙箱里连本机端口已经实测可行，**profile 不用改。**
4. Chrome 的进程组记进 `attempts/<id>/browser.pgid`，交给现有的回收逻辑。
   - 卡结束、取消或者超过 hard-duration 时，和执行器一起被杀。
   - serve 重启后，每一轮的 reconcile 也会照 `executor.pgid` 的方式检查它。

**改动量估计：**

- `internal/host/run.go` 加大约 60 行：启动、等端口、注入环境变量、清理。
- 新增 `internal/host/browser.go`。
- 文档里加一段「浏览器卡怎么写测试」。
- 沙箱 profile、定位表、渠道一概不动。

**测试代码要改的地方：**

TestSSOBrowserTwoRPs 这类测试，要从 `launch()` 改成「有 `CARDEX_BROWSER_CDP` 就 `connectOverCDP`，没有就照旧 `launch()`」。这样在本机直接跑和在卡里跑都兼容。

**还考虑过、但没选的做法：**

- **A. 浏览器卡直接用 `card add --sandbox off`。** v2.1.0 已经支持，今天就能用，Chrome 自己的沙箱也是开着的。代价是这张卡的执行器（也就是模型）失去 Cardex 对写入范围的限制。可以作为过渡，但不适合长期默认。
- **B. 给 profile 多放开几个写路径，再加 `--no-sandbox`。** 违反「不用 --no-sandbox」的要求，否决。
- **C. 只给 Chrome 子进程豁免外层沙箱。** seatbelt 不支持「子进程不继承沙箱」，做不到。

## 4. 风险

1. **本机端口暴露。** CDP 端口在这张卡运行期间只对本机开放，但本机任何进程都能连上，而连上就能完全控制这个浏览器。
   - 缓解：浏览器用一次性的用户数据目录，不带登录态，卡结束就杀。
   - 更进一步：host 不直接暴露 CDP，改成跑 Playwright 的 `launchServer`。它会生成一个带随机路径的 ws 地址，没有 `/json` 端点可以拿来探测。这样 host 要依赖 node 和 Playwright，可以放到第二版。
2. **执行器能拿浏览器访问外网。** 现在执行器本身就有网络权限，所以这不算新增的风险。
3. **浏览器不在 Cardex 的写入限制里。** 它能写的地方取决于 Chrome 自己的沙箱，加上 host 给的用户数据目录。这和今天在 Cardex 外面跑测试是同一个水平。
4. **测试要配合改。** 不改的测试还是会自己 `launch()`，在卡里照样崩。建议 doctor 或者卡的提示里明确写出来。

## 5. 验证方法

1. 在 Cardex 外复现，就是第 2 节那张表。脚本：
   ```sh
   W=$(mktemp -d /tmp/chromesb.XXXX); C="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
   printf '(version 1)\n(allow default)\n' > $W/open.sb
   sandbox-exec -f $W/open.sb "$C" --headless=new --user-data-dir=$W/ud --dump-dom 'data:text/html,hi'
   # 预期：sandbox initialization failed: Operation not permitted
   "$C" --headless=new --user-data-dir=$W/ud2 --remote-debugging-port=9333 about:blank &
   sandbox-exec -f $W/open.sb /usr/bin/curl -s http://127.0.0.1:9333/json/version
   # 预期：返回 Browser: Chrome/...
   ```
2. 实现之后加两个测试，都只在 macOS 上跑：
   - `TestBrowserCardCDP`：一张 `--require browser` 的卡在沙箱里 `connectOverCDP`，打开一个 data: 页面读 DOM。
   - `TestBrowserCardReaped`：卡被取消或超时后，`browser.pgid` 对应的进程组被清掉。
3. 真机验证：在一张 `--require browser` 的卡里跑 TestSSOBrowserTwoRPs，预期不再出现 `target_crashed`。同时 `ps` 能看到 Chrome 的各种子进程正常，没有 `--no-sandbox`。

## 6. NAS 的 Linux 容器（另一个问题，顺带说明）

Linux 上的 Chrome 沙箱靠的是 user namespace 加 seccomp。普通 Docker 默认的 seccomp profile 会拦 `clone(CLONE_NEWUSER)`，于是报 `No usable sandbox`。

不用 `--no-sandbox` 的常见解法有两种，按放开程度从小到大：

1. 容器使用 Chrome 官方的 seccomp profile（`--security-opt seccomp=chrome.json`）。
2. 宿主允许非特权用户创建 user namespace（`kernel.unprivileged_userns_clone=1`；Ubuntu 24.04 以上还要处理 AppArmor 的 `restrict_unprivileged_userns`）。

这部分涉及 NAS 上 Docker 的配置，按约定我没碰，也没碰标签为 20261112r5 的资源。要做的话应该交给基础设施。

## 7. 回滚

- 方案是新增的、按卡的标记生效。没有 `--require browser` 的卡完全走原来的路径。
- 回滚就是回退这次提交，或者干脆不给卡加 `browser` 标记。
- 不涉及数据库迁移、沙箱 profile、定位表或渠道。

## 8. 等 Otto 决定

1. 选第 3 节的方案（推荐），还是先用做法 A（`--sandbox off`）过渡。
2. 选了第 3 节的话，第一版用 CDP 端口，还是直接上 `launchServer`。
3. NAS 容器那边要不要交给基础设施处理。

## 实现状态（2026-11-13）

Otto 已批准方案 1（host 在沙箱外启动 Chrome，执行器 connectOverCDP）和第一版用 CDP 端口。实现在分支 `feat/browser-card-cdp`：

- `internal/host/browser.go`：找 Chrome、启动、读 `DevToolsActivePort`、注入 `CARDEX_BROWSER_CDP` / `CARDEX_BROWSER_CDP_WS`、写 `browser.pgid`、结束时杀进程组并删临时目录。
- `internal/host/run.go`：`--require browser` 的卡在执行器启动前拉起 Chrome，`defer` 收尾。
- `internal/host/reconcile.go`：host 挂掉后，reconcile 连带清理这次执行的 Chrome。
- `internal/route/select.go`：`browser` 是 host 提供的能力，不参与模型能力过滤，所以不用改定位表。

**升级到 launchServer 的位置**：`browser.go` 顶部注释写明了。只需要替换 `launchBrowser` 的启动命令和端点发现，改为注入 `CARDEX_BROWSER_WS`；环境变量注入、`browser.pgid` 和清理逻辑不变。

NAS 容器里的 Chromium 问题按 Otto 的决定先不处理。

