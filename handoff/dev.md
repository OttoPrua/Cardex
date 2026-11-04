# grok-toolcallid-allowrule-20261105

Writer: Codex. Base: local main `5382702`. Branch:
`clouddev/grok-toolcallid-allowrule-20261105`. Worktree:
`/Volumes/Storage/Development/Cardex/worktrees/grok-toolcallid-allowrule-20261105`.
Storage UUID verified: `4F8C8D1A-DB32-4DE7-95A9-54B80B64E756`.
Main's uncommitted `attempt_control*.go`, `tshare_cancel_closeout_test.go` and
other existing files were not modified, staged or stashed.

Verdict: **PARTIAL**. Both scoped code changes and focused regressions are
complete. Full-suite acceptance is blocked by five reproducible baseline
failures; exact incident stdout replay is unavailable. Do not install yet.

## Findings and changes

### Grok completion parsing

The three task records confirm exit 0, one end/end_turn and these first rejects:

| Task | stdout line/event | Reject |
| --- | --- | --- |
| t1104-0450-3434 | 1110 | tool_call_update / tool_identity / toolCallId / string |
| t1104-0213-d02a | 474 | same |
| t1104-0213-4bcd | 921 | same |

`tool_identity` is a lifecycle rejection, not a rejection of JSON string IDs.
The old parser rejected every update after a terminal tool status. Grok's
streaming terminal and tool completion layer can both emit completion updates.
The parser now remembers the terminal status and accepts a repeated **identical
terminal status for an already known ID**. Unknown/empty IDs, duplicate starts,
reopening, contradictory terminal statuses, unknown statuses, null status after
closure, malformed schemas and post-end activity still fail closed. The six-field
JSON schema and overall end/end_turn requirements are unchanged.

Evidence limitation: original stdout was **not found** in these cards' logs,
control records, tmp or evidence directories. The existing runner keeps stdout in
memory and its early Grok outcome path writes diagnostic summaries, not the raw
stream. Therefore the exact rejected lines cannot be claimed recovered or replayed.

Installed Grok reports `1.0.40 (eb1a2256660d)`. A matching original AU1 session was
found at `~/.grok/sessions/%2FVolumes%2FStorage%2FDevelopment%2FUkusikAuth/01a1a361-7de1-7882-9439-267da71f4d97/updates.jsonl`;
its timestamps match attempt `at5d969e087215af2c`. Line 93 supplies the redacted
native fixture `cmd/cardex/testdata/grok-1.0.40-terminal-update.json`.
The test projects that actual native event into streaming JSON; its preceding
terminal-runner update is synthetic. IDs, command, paths, description, output
bytes/text and byte count are replaced with harmless fixture values.

Upstream references checked at `2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8`:

- [Headless six-field update projection](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-pager/src/headless/reducer/acp.rs#L134)
- [Streaming terminal completion notification](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-shell-terminal/src/streaming_local_terminal.rs#L723)

That public snapshot has a different SOURCE_REV from the installed binary, so
it supports the producer mechanism, not an exact-version replay. The regression
reproduces the same diagnostic signature before the fix and passes afterward;
attribution of all three historical holds to this precise sequence remains
unverified without their original stdout.

### Generated review allow rules

All three held reviews (`t1104-0235-a7f5`, `t1104-0338-a1df`,
`t1104-0400-2377`) inherited these extra rules from the live configuration's
`type_defaults.design-review.allowed_tools`:

```text
Bash(bash /Users/ottoprua/.cardex/verify-mirror-fingerprint.sh:*)
Bash(bash ~/.cardex/verify-mirror-fingerprint.sh:*)
```

The generator now omits only those exact helper rules for newly generated local
reviews where the directory exists and neither `.claudego-fingerprint` nor
`.claudego-scripts` exists. This follows the review template's existing local
workspace exemption. Remote, partial mirror, dangling-marker and unreadable
workspace cases retain the rules. All other custom rules remain untouched and
unknown grammar still fails before Grok invocation. Both planned and legacy
review generation paths use the same helper.

This is narrower than widening `grokFrozenAllowRules`: no new shell execution
grant is admitted. Shared configuration and already persisted review cards are
unchanged. The three existing held cards will not repair themselves on install;
their owner must separately decide whether to create a successor or amend them.

## Validation and review

- Focused generator/allow-rule tests: PASS.
- New terminal regression before parser fix: FAIL with the reported
  `invalid_event_shape_before_end / tool_identity / toolCallId / string`.
- New terminal and existing nullable-output/unknown-rule regressions after fix: PASS.
- Full suite (`CARDEX_CANDIDATE_BIN=<candidate> go test -v -timeout 20m ./...`):
  FAIL, 627.582s. It reported six top-level failures. One was the old native
  lifecycle test's blanket rejection of repeated completion; updated to accept
  identical terminal status and explicitly reject contradictory status, then
  rechecked successfully with all focused regressions.
- The remaining five failures were reproduced with `go test -overlay` substituting
  the exact `5382702` versions of the only modified production files (`grok.go`
  and `runner.go`), without modifying the worktree or installed binary:

  | Existing failing test | Observed baseline failure |
  | --- | --- |
  | TestInstalledD63SameActionRetryStaysRed | Requires installed binary SHA d63f12…, actual d0dd6795… |
  | TestDisclosureFieldsReachUI | control_error has no board UI consumer/exemption |
  | TestEveryTerminalEmitSiteWrapsCostTelemetry | Existing Grok diagnostics-write-failure hold lacks withCostTelemetry |
  | TestGrokBuild105CompleteStdoutOmitsAncillaryWarningStderr | Invocation adds GrokAllowPolicy; old equality check expects nil |
  | TestNativeCompletionProcessAndConsumer/grok-build/completion_loss | Read-only ACP fake supplies a terminal despite headless fixture's missing end |

  These baseline failures were retained, not bypassed or weakened. Full-suite
  PASS is **not claimed**. Final focused run: PASS (1.372s).
- `go vet ./...`: PASS.
- Candidate build and `cardex version`: PASS (`0.10.43`, unchanged version string).
- Review method: author adversarial review, not independent review. Concrete
  risks examined: accepting contradictory tool outcomes and broadening shell
  permissions. Negative regressions cover both; the allow whitelist is unchanged.
- No paid model call, task requeue, installed-binary replacement or launchd action.

Logs and candidate:
`/Volumes/Storage/Development/Cardex/artifacts/grok-toolcallid-allowrule-20261105/`.
`go-test.log` is the complete full-suite result; `baseline-failing-checks.log`
contains the baseline comparison; `focused-final.log` contains the final focused
PASS. The initial unconfigured suite was interrupted at 322s after missing
`CARDEX_CANDIDATE_BIN` failures and retained as `go-test-sandbox-interrupted.log`.
The configured run passed those candidate-binding checks. No inference that
the first run's duration itself represented a code deadlock is made.
Candidate SHA-256:
`f72434c6de74886f47a1e122c03c93f561a8d958b5c0e7159e05044aa8c99fbf`.
Installed binary observed SHA-256:
`d0dd6795e249e21d46aa6bd531010270368f7d4008782626e812b7f3fcd9819b`.

## Installation and rollback — only after Otto confirms

These commands are instructions, **not executed actions**. No launchd restart
is included; an already running scheduler keeps its loaded binary. Coordinate
any later service restart and real-card verification with its runtime owner.

Build, if rebuilding is needed:

```sh
cd /Volumes/Storage/Development/Cardex/worktrees/grok-toolcallid-allowrule-20261105
GOCACHE=/Volumes/Storage/Development/Cardex/cache/grok-toolcallid-allowrule-20261105/go-build \
  go build -trimpath -o /Volumes/Storage/Development/Cardex/artifacts/grok-toolcallid-allowrule-20261105/cardex ./cmd/cardex
```

Install after explicit approval, preserving the then-current binary:

```sh
(
  set -eu
  cardex_artifacts=/Volumes/Storage/Development/Cardex/artifacts/grok-toolcallid-allowrule-20261105
  test ! -e "$cardex_artifacts/cardex.before-install"
  cp -p /opt/homebrew/bin/cardex "$cardex_artifacts/cardex.before-install"
  shasum -a 256 "$cardex_artifacts/cardex.before-install" "$cardex_artifacts/cardex"
  install -m 0755 "$cardex_artifacts/cardex" /opt/homebrew/bin/cardex.grok-fix-new
  mv /opt/homebrew/bin/cardex.grok-fix-new /opt/homebrew/bin/cardex
  /opt/homebrew/bin/cardex version
  shasum -a 256 /opt/homebrew/bin/cardex
)
```

Rollback after an approved installation:

```sh
(
  set -eu
  cardex_artifacts=/Volumes/Storage/Development/Cardex/artifacts/grok-toolcallid-allowrule-20261105
  test -f "$cardex_artifacts/cardex.before-install"
  install -m 0755 "$cardex_artifacts/cardex.before-install" /opt/homebrew/bin/cardex.grok-fix-rollback
  mv /opt/homebrew/bin/cardex.grok-fix-rollback /opt/homebrew/bin/cardex
  shasum -a 256 "$cardex_artifacts/cardex.before-install" /opt/homebrew/bin/cardex
)
```

Runtime and real-card acceptance remain unverified. The branch intentionally
inherits the local main commits requested by the task; when opened against
remote main, the draft PR also includes those pre-existing unpublished commits.
Do not treat this draft as approval to merge or install that inherited scope.
