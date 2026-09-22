# Owner mixed routing

Enable `owner_mixed_routing: true` alongside the existing `owner_routing_enforced: true` configuration. Keep the historical Grok/Kimi/Cursor/tier settings unchanged: old cards still use their frozen policy. All four executors (Codex, Grok Build, Cursor, Antigravity) must be enabled. No task history or running attempt is migrated.

New tasks carry `work_class`. Classification is explicit metadata, independent of backend/general and prompt wording:

| work_class | Primary | Quota-only fallback |
|---|---|---|
| development | Grok Build `grok-4.7` / high | Cursor `grok-4.7-high` |
| simple-development | Grok Build **pinned** `grok-4.6` / high | Cursor `cursor-grok-4.6-high` |
| gpt-complex | Codex `gpt-6-astra` / high | None |
| gpt-short | Codex `gpt-5.6-sol` / xhigh (max for higher risk or explicit max) | None |
| management | Antigravity `gemini-3.8-flash-high` / high, plan mode | None |

An explicit `-effort medium` permits the lower-complexity Astra lane. New sequence tasks default to `development` (including frontend/general work); coordinate/progress-pull default to management, prompt-assembly to gpt-complex, and design-review to gpt-short. Use the class instead of guessing from prompt length or the old Sonnet/Haiku tier. Continue declaring `route_class` and `risk_class`; use a single prompt or `fresh_steps: true` for automatic routing. Existing explicit provider/model pins, sessions, cross profiles, admission/custody, review requirements and task budgets retain their authority. Mixed primary work does not create a mandatory review chain. Automatic Astra/Sol preserves the existing 65% Codex budget stop and holds when provider evidence is missing/stale. Historical Sol review one-call counters remain scoped to those historical lineages.

Grok subscription runs first. Switching quota sources requires an explicit exhausted-quota diagnostic, complete observation, zero semantic/model/tool activity, unchanged workspace, and no writer/process residue. A bare 429, rate limit, authentication/401, refusal, transport failure or unknown terminal is **not** that evidence and cannot switch providers. Cursor receives exactly the same Grok model and high effort. A Cursor failure has no further fallback. No deliberate quota-exhaustion probe is required.

Read the real configured route without enqueuing a task or calling a provider:

```sh
cardex route -root ~/.cardex -work-class development
cardex route -root ~/.cardex -work-class simple-development
cardex route -root ~/.cardex -work-class gpt-complex -effort medium
cardex route -root ~/.cardex -work-class gpt-short -effort max
cardex route -root ~/.cardex -work-class management -type progress-pull
```

Use the same classification for automatic or manual work:

```sh
cardex add -root ROOT -work-class simple-development -route-class general -risk-class ordinary -hold 'Scoped task with acceptance criteria'
cardex cmd -root ROOT TASK_ID
```

For Yvonne/emit consumers, include `"work_class":"simple-development"` (or another table value) in each task JSON and omit `runner` for policy routing. A new native Grok workflow writer with the mixed admission marker and no model/effort/attempt/session gets 4.7/high before Goal admission; explicit pins and previous execution identity are preserved. Workflows with an explicit executor keep that executor and do not acquire automatic cross-provider fallback authority.

Management canary: in an isolated root with a copied, validated configuration, create `-type progress-pull -work-class management -route-class general -risk-class ordinary`, then run that exact ID. `agy models` must advertise the pinned ID; availability of another Opus/Gemini ID cannot substitute for it. The runtime records requested identity; provider-reported actual identity remains limited by what the terminal actually reports.

Rollback: disable the switch to stop creating mixed cards. Frozen mixed cards retain their identities; reconcile them before installing an older binary that does not understand `work_class`. The switch does not rewrite any card or grant permission to run held work.
