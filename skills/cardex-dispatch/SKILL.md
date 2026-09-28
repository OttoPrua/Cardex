---
name: cardex-dispatch
description: Set up and operate Cardex from a Codex or Hermes management session; inventory existing CLI subscriptions, recommend and save task presets, dispatch bounded tasks, and follow results. Use when the user asks to configure Cardex or delegate work through its local queue.
---

# Cardex dispatch Agent

The current Codex or Hermes session is the manager; Cardex is a local CLI queue; provider CLIs execute cards. Managing in Codex does not make another provider's subscription a native Codex model. Keep the selected manager, machine, Cardex binary/data root, and project explicit. This skill works on its own; no other skill is required.

## First-use configuration

- Reuse an existing installation/configuration. Locate `cardex` (default installer paths: `~/.local/bin/cardex` or `%LOCALAPPDATA%\Cardex\bin\cardex.exe`). Read `cardex presets` and `cardex list -json` for the chosen root. Do not reset a populated queue or overwrite existing routing policy.
- Run `cardex setup -inventory`: it detects CLI paths only; `auth: not_checked` is not proof of a subscription, login, or model availability. Combine it with subscriptions already stated by the user. Ask only for missing choices. Verify official CLI login status/model catalogs without printing credentials. Install or log in only to the user's selected services.
- For a new Claude/Codex root, `cardex setup -runner claude|codex [-root PATH] [-bin PATH]` initializes a starter. It refuses existing config; does not install/login/call models. Codex starter uses its CLI default model and medium effort. For other configured runners, use `cardex init`, then preserve other keys while adding the needed runner fields from the matching release's configuration reference (https://github.com/OttoPrua/Cardex/blob/main/docs/config.md; select the installed version's tag before relying on its schema). `-root` must be repeated on subsequent commands or set `CARDEX_ROOT`.
- To recommend models, consult current coding evaluations at https://artificialanalysis.ai/ (e.g. Terminal-Bench), then intersect candidates with the actual CLI catalog and subscription entitlement. Compare evaluation harness, reasoning effort, cost, latency, quota, task fit and real-run reliability; a ranking label is not a CLI model ID. Record source URLs and date. If live sources are unavailable, disclose that and do not claim a current ranking.
- Show a compact table for `routine`, `development`, `complex`, and `management`: runner, exact available model, supported effort, review choice and reason. Reuse a capable model across levels when appropriate; do not require four subscriptions. Stay within the already-authorized providers and data scope. Never transfer private project data to a new provider just because it ranks higher.
- Where the user has authorized recommended defaults, display and adopt the recommendation without repeated approval; manual choices override it. Persist with `cardex presets -file FILE`, whose input is the inner map: `{"routine":{"runner":"codex","model":"ACTUAL_AVAILABLE_ID","effort":"medium","review_after":false,"note":"Reason; source URL; checked YYYY-MM-DD"}}`. Replace placeholders using current evidence. Import updates same-name presets, preserves other config keys and writes a backup. It does not validate a real model request. Read back with `cardex presets`.
- Subsequent tasks reuse saved presets. Refresh only when requested, entitlement/model availability changes, or an actual failure makes it necessary. Cardex itself does not fetch rankings or automatically revise presets. Preserve the user's edits.

## Subscription configuration keys

For a fresh root, preserve the JSON object and add only selected CLI settings. Provider credentials remain in each CLI; never copy them into prompts. Read the version-matching [configuration reference](https://github.com/OttoPrua/Cardex/blob/main/docs/config.md) for advanced options.

| Runner | Minimal configuration after login/catalog checks |
|---|---|
| `claude` | `claude_bin`; starter: `setup -runner claude` |
| `codex` | `codex_bin`; starter: `setup -runner codex` |
| `grok-build` | `grok_build_bin`, `grok_build: {"enabled":true,"model":"ACTUAL_ID","effort":"high"}` |
| `cursor` | `cursor_bin`; preset model is the exact catalog ID including its effort suffix |
| `kimi-cli` | `kimi_cli_bin`, `kimi_cli_model`, `kimi_cli_effort` supported by that model |
| `agy` | `antigravity_bin`, `antigravity: {"enabled":true,"effort":"high"}` |
| `opencode` | `opencode_bin`, `opencode_model` as the actual `provider/model` |

Replace ACTUAL_ID and paths with verified values. For a fresh non-Claude-only installation, explicitly set `claude_bin` to an empty string; always dispatch with the selected runner's preset. Do not remove Claude from an existing shared queue just because one preset uses another runner. API-compatible subscription profiles are optional: `cardex engines` lists presets, and `cardex engines add NAME` adds a selected profile; prefer `auth_env`/`auth_file` over inline credentials. Consumer chat subscriptions do not automatically grant API/CLI access.

Preset `effort` is intentionally empty for Cursor (encoded in model ID) and agy (uses `antigravity.effort`). Grok has no max effort; OpenCode uses provider variants. After configuration, run `doctor` and a no-model dry-run. Only run an actual provider task within the user's authorized subscription/data scope, and report that result separately from configuration success.

## Dispatch and follow through

1. Recover the goal, project directory, allowed effects and an appropriate real check from the request and current evidence. Check `list -json` for existing work; avoid duplicate writers. Routine implementation can be one bounded card, including diagnosis, code, checks and fixes.
2. Select an existing preset. Use `cardex add -preset development -dir PROJECT -title TITLE -file TASK_FILE -dry-run` to inspect the normalized card. The file contains the full bounded task; `---` on its own line splits steps. Codex supports a single step or `-fresh` multi-step tasks; fresh steps must get context from files rather than prior conversation.
3. Remove `-dry-run` to enqueue when authorized. If preparing rather than executing, use `-hold`; a background scheduler can start queued cards immediately. Use `cardex run TASK_ID` for this task only. Bare `run` processes the ready queue. Put options before the positional ID or prompt. Explicit `-root PATH` belongs on every command using a custom root.
4. Follow `list -json`, `log TASK_ID`, and `digest`. Read the resulting artifact and actual check output before claiming success; queue `done` alone does not prove deployment or user-path acceptance. Report result, validation, remaining uncertainty and task ID concisely.

Single-task flags can override saved settings for the same runner (Codex: `-codex-model`, `-effort`, `-review-after=false`; Grok: `-grok-model`, `-grok-effort`). For Cursor and agy presets, omit `effort`: Cursor encodes it in its model ID; agy uses `config.antigravity.effort` rather than a per-card setting. A conflicting `-runner` is rejected. Updating a preset affects future cards, not existing ones. Do not override frozen task contracts or existing owner routing to force a preset through admission.

Review depth follows concrete risk. Ordinary bounded work can use author checks. `review_after=true` creates an extra review card using existing review routing; it is not proof of an independent model/reviewer and may consume another provider's quota. For data-loss, money, security, concurrency or an explicitly requested independent review, specify the actual review method and appropriate reviewer within authorization. Avoid duplicate automatic review when a reviewer is already assigned.

On failures, retain changes and inspect logs/state before retrying. A held or uncertain attempt is not permission for a duplicate writer or provider switch. After two failed rounds on the same issue, summarize evidence and reassess. Do not buy services, publish, message others, change production data or expand provider/data recipients without applicable user authorization.

Windows native execution has platform limits: hosted PTY Goal and proof-gated automatic cross-engine fallback are unsupported. Check the shipped Windows guide before promising unattended behavior. Shell-based verification/SSH examples require their tools; do not mix WSL and native Windows paths.
