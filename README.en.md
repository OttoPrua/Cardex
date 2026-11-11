# Cardex

**Set a goal in one conversation and let multiple AI coding tools deliver it together.**

[中文](README.md) · **English** · [Install](#install) · [Supported tools](#supported-tools) · [Upgrade from V1](docs/v2/upgrade-from-v1.md) · [Download source](https://github.com/OttoPrua/Cardex/releases/tag/v2.0.0)

![Cardex: a Codex or Hermes management session hands work to local Cardex, which dispatches to configured coding tools.](docs/images/cardex-v2-overview.en.svg)

Cardex is a development orchestrator that runs on your machine. You set goals and answer key questions in a **Codex or Hermes management session**; Cardex splits the work into cards, picks models, dispatches, reviews, and merges, while each coding tool uses its own subscription or API. The scheduler and board never call a model themselves.

- **Reuse the subscriptions and APIs you already have.** Claude Code, Codex, Grok, Cursor, Kimi, OpenCode, and Antigravity plug in. Routing weighs tier, capability, and remaining quota, and `route explain` shows why a model was picked.
- **One card for small jobs, automatic planning for big ones.** A manual objective runs only the cards you add; an automatic one surveys, designs, and splits the work, and asks you only when a decision is needed.
- **Parallel work, merged only after it checks out.** Each attempt gets its own worktree and conflicting writes wait. A different model family reviews, and the project tests must pass before the objective branch moves.
- **The conversation can stop; progress stays.** Objectives, cards, attempts, and decisions live in local SQLite, and code lives in Git. After a quota or auth failure, work resumes from the card branch; with no suitable channel it waits or asks, and never silently switches tools.

## Install

**Recommended: paste this into a Codex or Hermes session that can operate your computer.**

```text
Help me install and configure Cardex in this conversation. Get the source from
https://github.com/OttoPrua/Cardex/releases/tag/v2.0.0 (or continue in the extracted folder with go.mod).
Read docs/v2/guide.md and skills/cardex-dispatch/SKILL.md. If this machine already has Cardex V1 data,
read docs/v2/upgrade-from-v1.md first. Then guide me step by step:
1. Confirm the management session, host, and data directory; build from source and run doctor.
2. List the execution CLIs you find, ask which subscriptions or APIs I want, and set up auth inside
   each tool (no keys pasted into chat).
3. Recommend T1–T3 models, a different model family for review, and quota reserves; save only after
   I confirm, then rerun doctor.
4. Recommend a manual or automatic objective, show the first card and its acceptance check, and ask
   before starting the scheduler.
```

**Manual** (Git and Go 1.24+):

```sh
git clone https://github.com/OttoPrua/Cardex.git && cd Cardex
go build -o ./cardex ./cmd/cardex && ./cardex --home "$HOME/.cardex-demo" doctor
```

A fresh `doctor` reporting missing channels or model placement is expected; continue with [the first objective](#run-a-first-objective). Choose the data directory with `--home` or `CARDEX_HOME`.

> **Platforms:** macOS is fully supported, with executors running in a seatbelt sandbox. Linux builds and runs, but executors are not sandboxed. Windows is not supported in V2 yet.

## Supported tools

| Coding tool | runner | How it connects |
| --- | --- | --- |
| Claude Code | `claude` | CLI login, or an Anthropic Messages compatible service |
| Codex CLI | `codex` | Signed-in account, or a Responses compatible provider |
| Grok Build | `grok` | Grok CLI account |
| Cursor Agent | `cursor` | Models available to your Cursor account |
| OpenCode | `opencode` | A configured `provider/model` (e.g. OpenCode Go, Chat Completions compatible services) |
| Kimi CLI | `kimi` | Kimi CLI account |
| Antigravity | `agy` | Antigravity CLI |

Subscriptions, pay-as-you-go APIs, and compatible gateways all connect through the matching tool; Cardex passes the model ID and does not translate protocols.

## Run a first objective

![An automatic objective: design, owner approval, parallel cards, review and tests, then merge into the objective branch; rejected work retries with review notes.](docs/images/cardex-v2-objective.en.svg)

Using Codex to write and Claude to review; replace the repo path, model IDs, and test command:

```sh
export CARDEX_HOME="$HOME/.cardex-demo"
./cardex project add --id demo --repo /path/to/repo --test-cmd 'go test ./...'
./cardex channel set --id my-codex --runner codex --model 'YOUR_AUTHOR_MODEL_ID' --concurrency 1
./cardex channel set --id my-claude --runner claude --model 'YOUR_REVIEW_MODEL_ID' --concurrency 1
./cardex channel check
```

Add an initial model placement to `$CARDEX_HOME/config.json` (example below), then update it later with `positioning proposals` / `positioning approve`.

```sh
./cardex objective add --id first-change --project demo --mode manual \
  --title 'Fix export notes' --origin 'codex:YOUR_THREAD_ID'
./cardex card add --objective first-change --kind dev --tier T2 \
  --title 'Add export examples' --body 'Update the export docs, keep behavior, and run the project tests'
./cardex serve --addr 127.0.0.1:0
```

`serve` starts the scheduler and a read-only board; open the printed address to follow progress. To check a dispatch, use `card show ID` and `route explain ID`.

> The board has **no authentication**. For remote viewing, bind to localhost or a private address such as Tailscale; do not expose `0.0.0.0` to the internet.

<details>
<summary>Initial model placement example</summary>

Merge into `config.json`; model IDs must match the channels. T1 suits fact checks, T2 routine development, T3 design and review.

```json
{
  "positioning": [
    {"id": "starter-dev", "model": "YOUR_AUTHOR_MODEL_ID", "family": "gpt",
     "tiers": ["T1", "T2"], "capabilities": [], "preference": 1},
    {"id": "starter-review", "model": "YOUR_REVIEW_MODEL_ID", "family": "claude",
     "tiers": ["T3"], "capabilities": [], "preference": 1}
  ]
}
```

</details>

[Guide](docs/v2/guide.md) · [Design](docs/v2/design.md) · [Dispatch skill](skills/cardex-dispatch/SKILL.md) · [Upgrade from V1](docs/v2/upgrade-from-v1.md)

## License

[PolyForm Noncommercial 1.0.0](LICENSE): free for personal use, study, research, and nonprofit organizations. Production use inside a company, paid client work, or offering it as part of a product or service needs a license; [open an issue](https://github.com/OttoPrua/Cardex/issues) describing your use. This is not an OSI open-source license; releases before 2026-08-03 (up to `b1ed92b`) remain MIT.

## Thanks

Cardex is shared in the [LINUX DO](https://linux.do) community. Thanks for all the feedback.
