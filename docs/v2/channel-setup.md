# 新增或检查一条渠道（给 Agent 的配置引导）

这份说明写给在本机替 Cardex 接入一个新 CLI 渠道的 Agent，也适合检查一条已有的渠道。每一步都有一条可以直接跑的验证命令。照着做完以后，`route explain` 里就能看到这条渠道被选中。

## 0. 先弄清 Cardex 用什么环境启动 CLI

`cardex serve` 一般由 launchd（macOS）或 systemd 拉起，它的环境和你的交互 shell 不一样：

- 不会读 `~/.zshrc`，所以 shell 函数、alias，以及在 zshrc 里 `export` 的令牌都看不到。
- `PATH` 由服务定义里的 `PATH` 决定。

先看清楚：

```
plutil -p ~/Library/LaunchAgents/com.cardex.serve.plist   # macOS
```

如果 CLI 依赖 zshrc 里的东西（比如令牌放在钥匙串里、启动时才读出来），就在服务 `PATH` 最前面那个目录（本机是 `~/.local/cardex-v2-shims/`）里放一个同名小脚本，只补上缺的那几项，然后 `exec` 真正的 CLI。不要改 Cardex 本身。下面是例子：本机的 `claude` 走 AnyRouter，令牌在钥匙串里。

```sh
#!/bin/sh
set -eu
: "${ANTHROPIC_BASE_URL:=https://anyrouter.top}"
if [ -z "${ANTHROPIC_AUTH_TOKEN:-}" ] && [ -z "${ANTHROPIC_API_KEY:-}" ]; then
  ANTHROPIC_AUTH_TOKEN=$(/usr/bin/security find-generic-password -s anyrouter-claude-token -w) || exit 72
  export ANTHROPIC_AUTH_TOKEN
fi
export ANTHROPIC_BASE_URL
exec "$HOME/.local/bin/claude-external" "$@"
```

改之前先备份原来的文件或软链接，比如 `readlink` 的结果存一份。脚本里不要写死令牌。

## 1. 确认 CLI 已登录，列出可用模型

| runner | 登录检查 | 列模型 |
|---|---|---|
| cursor | `cursor-agent status` | `cursor-agent --list-models` |
| grok | `grok models`（第一行就是登录状态） | `grok models` |
| opencode | `opencode auth list` | `opencode models` |
| claude（API 中转） | 直接调用 | `curl $ANTHROPIC_BASE_URL/v1/models -H "x-api-key: …" -H "anthropic-version: 2023-06-01"` |
| codex / kimi / agy | 跑一次冒烟测试 | 看各自的配置文件，或者看订阅页面 |

## 2. 用 Cardex 的真实参数做一次冒烟测试

参数要和 runner 的 `Build` 拼出来的一样（见 `internal/runner/<runner>.go`）。环境尽量贴近服务：用 `env -i` 只带服务定义里那几项，并且一定要设超时。例子：

```
env -i HOME=$HOME USER=$USER PATH="$HOME/.local/cardex-v2-shims:/usr/bin:/bin" \
  perl -e 'alarm 180; exec @ARGV' \
  cursor-agent --print --trust --output-format stream-json --model grok-4.7-high \
  --workspace /tmp/smoke --sandbox enabled "Reply with exactly: CARDEX_OK"
```

结果里出现 `CARDEX_OK` 才算通过。如果失败，先把失败原因和 `internal/runner/classify.go` 对一下，看看会被分成哪一类：

- **quota：** 整条渠道会被停用，直到额度重置。
- **transport：** 在同一条渠道上重试。
- **auth / permission：** 渠道会被标成不健康。

## 3. 建渠道

```
cardex channel set --id cursor --runner cursor --concurrency 6 \
  --model grok-4.7-high --model cursor-grok-4.6-xhigh --model auto --model claude-opus-5-5-high
cardex channel show cursor --json
```

- `--concurrency`：设成上游允许的并行数。填 0 表示 Cardex 本地不设上限，靠上游自己限流，被限流时当作 transport 重试。
- `--model`：可以写多个。第一个是这条渠道的默认模型，只指定渠道、不指定模型时就用它。
- 额度会单独耗尽的模型，比如订阅里单独计量的 Opus，放在列表最后，而且**不要**写进定位表。只有显式指定时才会用到它，这样它额度用完也不会拖着整条渠道一起被停用，因为额度停用是按整条渠道算的。

## 4. 加进自动路由池（定位表）

渠道建好以后，它只会在被显式指定时才用到。想让自动路由选它，要给它的模型加一行定位：

1. 用 `cardex positioning show --json` 拿到当前表。
2. 在 `$CARDEX_HOME/positioning/proposals/<id>.json` 写一份提案，格式是 `{"id","entries":[完整的新表],"diff","status":"proposed"}`。表要写全，不能只写新增的那几行。
3. 运行 `cardex positioning approve <id>`。
4. 再跑一次 `cardex positioning show`，确认新行已经在表里。

`family` 填模型的出品方，比如 anthropic、openai、grok、gemini、kimi。复核卡会避开和作者同一 family 的模型，所以 family 一定要填准。只改 `config.json` 里的 `positioning` 不会让当前表生效。

## 5. 收尾检查

```
cardex doctor
cardex channel list
cardex route explain <card>   # 第一张真实卡派出去之后
```

## 6. 模型会更新

渠道的模型列表和定位表都是静态的，CLI 出了新模型也不会被自动选上。建议隔一段时间重新跑一遍第 1 步，和 `channel list --json` 以及 `positioning show` 对比一下。有差别时，提一份新的定位提案，再请 Otto 批准。

## 7. 网络代理（统一入口）

所有出网都走 `~/.cardex/config.json` 里的同一个 `proxy` 段，包括 serve 自己的 git 和通知，以及每个渠道的 CLI 子进程：

```json
"proxy": {
  "url": "http://127.0.0.1:6152",
  "socks": "socks5://127.0.0.1:6153",
  "no_proxy": ["127.0.0.1", "localhost", "::1", "100.64.0.0/10", ".ts.net", ".local"],
  "channels": { "kimi": "direct", "agy": "http://127.0.0.1:7890" }
}
```

- cardex 每次运行都会把 `HTTP_PROXY`、`HTTPS_PROXY`、`ALL_PROXY`、`NO_PROXY` 设好，大小写两种写法都设；启动 attempt 时再按渠道覆盖一次。
- `channels` 里写 `direct` 表示这个渠道直连，会清空继承来的代理变量；写一个 URL 表示这个渠道单独走它。
- `no_proxy` 不写时，默认绕开本机、Tailscale 网段、`.ts.net` 和 `.local`。
- 没有 `proxy` 段时行为不变。地址写错时 cardex 会报 warning，不会悄悄改成直连。
- 渠道子进程每次启动 attempt 都会重读配置，改完立刻生效；serve 自己的 git 和通知要重启 serve 才换出口。用 `cardex proxy show` 查看每个渠道最终用哪个出口，用 `cardex proxy check` 测代理端口通不通。
