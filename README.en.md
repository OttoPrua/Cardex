# cardex

**Queue local Agent tasks. Track progress, pause work, and take over when needed.**

[中文](README.md) | **English** · [Download](https://github.com/OttoPrua/cardex/releases/latest) · [Setup guide (中文)](docs/getting-started.md) · [Dispatch Agent (中文)](docs/dispatch-agent.md)

Cardex is a local command-line scheduler. It saves goals or steps as task cards, invokes your installed and authenticated Claude Code / Codex CLI, and keeps task state and logs on disk. It ships as a single Go binary. Scheduling and the board do not call a model; task execution uses your provider's quota.

![Workflow diagram: a goal enters a local queue through a dispatcher, runs through an execution CLI, and reports state to the board. This is a diagram, not a product screenshot.](docs/images/cardex-flow.svg)

## What it does

| Need | Feature |
|---|---|
| Delegate a fix or feature | Save a task with `add`, execute it with `run ID` |
| Run a sequence of steps | Separate prompts with `---`; Claude can resume a shared session, subject to executor-specific support |
| Continue after a quota reset | Supported runners record cooldowns and resume; authentication failures or uncertain results can require intervention |
| See what is happening | `list`, `log ID`, and the local web `board` |
| Have an Agent organize your tasks | A separate dispatcher role and the bundled [cardex-dispatch skill](skills/cardex-dispatch/SKILL.md) |
| Use advanced orchestration | Optional runners, dependencies, reviews, and [workflows](docs/workflows.en.md) |

## Start from a Codex or Hermes management session

Use a dedicated **Codex or Hermes session as the manager**. Cardex keeps and schedules the local queue; provider CLIs execute tasks. The manager's model and a task's execution model are independent.

Install the binary and dispatch skill on that machine. macOS / Linux:

```sh
curl -fsSL https://github.com/OttoPrua/Cardex/releases/latest/download/install.sh -o /tmp/cardex-install.sh && sh /tmp/cardex-install.sh --manager codex
```

Windows PowerShell:

```powershell
& ([scriptblock]::Create((Invoke-RestMethod -ErrorAction Stop https://github.com/OttoPrua/Cardex/releases/latest/download/install.ps1))) -Manager codex
```

For Hermes, replace `codex` with `hermes`. The installer verifies the archive, installs the binary and skill, inventories local CLIs, then instructs the **deploying Agent to continue in the same conversation**: identify existing subscriptions, guide login, propose model/effort/review routes, ask whether to adopt them, and save the accepted presets. Existing configuration and backups are preserved.

You can give your Agent this request before installation:

```text
Install and configure Cardex using https://github.com/OttoPrua/Cardex/blob/main/docs/getting-started.md.
After installation, read the installed cardex-dispatch skill and continue in this conversation.
Ask step by step which existing subscriptions I want to use, help configure them, then show
routes based on verified available models and ask whether I want to adopt them.
Save after confirmation, respect my overrides and reuse the presets. Do not stop at "installed".
```

The management Agent researches recommendations; Cardex does not automatically fetch rankings or change models. Once the guide has saved your presets:

```sh
cardex presets
cardex add -preset routine -dry-run -dir . "Read this project and explain its entry point and run command. Do not edit files."
cardex add -preset routine -dir . "Read this project and explain its entry point and run command. Do not edit files."
cardex run TASK_ID               # Use the ID returned by add; this calls a model
cardex log TASK_ID
cardex board                    # Open http://127.0.0.1:8787
```

`routine` must exist first. `add -dry-run` prints a card without saving or executing it. `run TASK_ID` targets one card; bare `run` processes the ready queue. If a scheduler is active, queued cards may start immediately; use `add -hold` to prepare work.

For a minimal CLI setup, `cardex setup -runner claude` or `-runner codex` creates a new configuration; follow with `cardex doctor`. Setup refuses to overwrite existing configuration. It does not install, authenticate, or invoke a model. Codex starts with its CLI default model and medium effort. The [setup guide (中文)](docs/getting-started.md) covers PATH, manual downloads, subscriptions, presets and troubleshooting.

Build from source with Go 1.24+: `go build -o cardex .` (use `cardex.exe` on Windows).

## Windows status

Releases include a native Windows executable. **This does not mean every Windows feature has been fully validated.** Check configuration and basic task execution on your machine before unattended use; real model execution also depends on your CLI, authentication, permissions, and project tools.

- `install-launchd` is macOS only. Use foreground `daemon` or Windows Task Scheduler on Windows.
- Hosted PTY Goal is unsupported on Windows.
- Automatic cross-engine transitions requiring strict process-exit proof are unavailable. See the [Windows guide](docs/windows.md) for native test coverage and process lifecycle behavior.
- Advanced Bash / SSH / rsync examples require those tools. Keep native Windows and WSL paths separate.

## Configuration and daily use

Data lives under `~/.cardex` by default, including `config.json`, task records, and logs. Select another root with `CARDEX_ROOT` or `-root` on each command. Useful starting settings are `max_parallel` (1), `step_timeout_min` (60 minutes), and `poll_interval_sec` (300 seconds). Run one successful task before tuning concurrency or model routing.

```sh
cardex hold TASK_ID              # Pause a card
cardex release TASK_ID           # Requeue it; does not execute it immediately
cardex daemon                   # Poll continuously; Ctrl+C to exit
```

For background scheduling on macOS, use `cardex install-launchd`. For existing installations, edit the existing configuration rather than replacing it with a starter file. See the [configuration reference](docs/config.en.md) for runner and model settings.

## A dedicated dispatch Agent

Use a separate Agent session to clarify scope, inspect the queue, create bounded tasks, and follow results. Give execution Agents only the context needed for their task. The repository includes a copyable role prompt, examples, and installation instructions in the [dispatch guide](docs/dispatch-agent.md), plus a portable [cardex-dispatch skill](skills/cardex-dispatch/SKILL.md).

## Documentation

| Document | Contents |
|---|---|
| [Getting started (中文)](docs/getting-started.md) | Installation, setup, first task, troubleshooting |
| [Dispatch Agent (中文)](docs/dispatch-agent.md) | Role prompt, examples, skill installation |
| [Configuration reference](docs/config.en.md) | Configuration keys and templates |
| [Advanced guide](docs/guide.en.md) | Assembly, coordination, progress, review routing, quota, and runners |
| [Recommended workflows](docs/workflows.en.md) | Dependencies, write domains, independent review, Goal, and integration |
| [Mixed routing (中文)](MIXED_ROUTING.md) | Optional routing for established multi-provider setups |
| [Runtime internals](docs/internals.en.md) | Recovery, retries, permissions, and persistent state |
| [Changelog](docs/changelog.en.md) | Version history |

The optional [perlica-low-token-manager skill](skills/perlica-low-token-manager/SKILL.md) supports long-running management sessions. Cardex was formerly ClaudeGo; existing aliases and data migration remain supported. See the [setup guide](docs/getting-started.md#已有安装与旧名称).

Development checks: `go test ./...`; `make test` runs the Bash integration suite with mock CLIs.

## License

[PolyForm Noncommercial 1.0.0](LICENSE) — **free for personal use, commercial use needs a license**.

- **No need to ask**: personal use, study and research, hobby projects, and use by charitable,
  educational, or public research organizations.
- **Needs a license**: internal production use at a company, using it to deliver paid client
  work, or shipping it (or a derivative) as part of a product or service. Open an
  [issue](https://github.com/OttoPrua/cardex/issues) describing the intended use.

Two caveats: this is **not** an OSI-approved open-source license (it restricts commercial use),
so don't wave it through your company's dependency compliance as "open source"; and versions
published before 2026-08-03 (through commit `b1ed92b`) remain MIT — that grant is irrevocable
for those versions, and this change applies only to what follows. See [LICENSE](LICENSE).

## Acknowledgements

This project is shared with the [LINUX DO](https://linux.do) community — thanks to everyone there for the feedback.
