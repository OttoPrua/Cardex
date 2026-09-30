# Grok first-rejection locus — 2026-09-22

## Source commit

Branch `codex/mixed-routing-20260922`. Parent and current `HEAD`: `625900ceffd0b30b57d6c70287afa2f7ddd48d11`.

The stage commit was not created. `git add` / `git commit` cannot create `index.lock` in `/Users/ottoprua/Projects/Cardex/.git/worktrees/mixed-routing-20260922` (`Operation not permitted`). The three files below are modified in the worktree and are not staged. Intended subject: `Record the first content-free Grok stream rejection locus`. Intended body: held `stream_incomplete` summaries could not tell a parser rejection from a later sticky `ObservationComplete` flag; keep the existing first-defect category and add closed coordinates only.

## Diff scope

- `grok.go` — extend `grokBuildDiagnostics` inside `parseGrokBuildJSONLChannels`. No new stream store, no debug flag, no change to invoke buffers or the runner held summary path beyond the existing `grok_diagnostics` object.
- `grok_observer_105_test.go` — locus expectations on the existing task/event readback, plus adversarial privacy and business-byte checks.
- `docs/handoffs/cardex-grok-stream-shape-20260922.md` — this receipt.

No other repos, cards, config, proxy, or installed binaries.

## Diagnostic fields

`defect_source` remains the bounded source of the first defect: `none`, `stdout`, `stderr`, `unknown`, `observation`.

The new fields are written only on that same first defect. A later record does not move them because `ObservationComplete` is still false.

| Field | Meaning |
| --- | --- |
| `reject_line` | 1-based physical line in the combined observation. `\n` separates lines and CRLF is one line. Blank lines advance this counter. `0` when no record was rejected. |
| `reject_event` | 1-based index of non-blank scanned records. Blank lines do not advance it. Scanner loss uses the next unscanned line and event index. `0` for `missing_end` and for a legal stream. |
| `reject_event_type` | Parser event name, or `unknown`, `absent`, `none`. |
| `reject_schema` | Closed reason below. |
| `reject_json_type` | `string`, `number`, `object`, `array`, `boolean`, `null`, `absent`, `none`, `invalid`. |
| `reject_field` | Allowlisted field name, `unknown`, or `none`. |
| `reject_unknown_keys` | Count of non-allowlisted keys, saturated at 32. Plan-entry rejections also count disallowed keys inside entries. Names are not stored. |

`reject_event_type` is `absent` only when a JSON object has a missing, empty, or non-string `type`. A wire type of `absent`, `none`, or `unknown` is stored as `unknown`. `none` means the defect has no event type (malformed JSON, scanner loss, missing end, or no defect).

`reject_schema` is one of: `none`, `unknown`, `malformed_json`, `missing_type`, `type_not_string`, `decode`, `type_mismatch`, `missing_field`, `unexpected_field`, `invalid_meta`, `invalid_nested`, `invalid_shape`, `unknown_event`, `duplicate_end`, `abnormal_stop`, `disallowed_after_end`, `legacy_tool`, `tool_identity`, `tool_status`, `tool_unclosed`, `scanner_loss`, `stderr_scanner_loss`.

`legacy_tool`, `tool_identity`, `tool_status`, and `tool_unclosed` explain an `invalid_event_shape_before_end` defect whose JSON shape is otherwise legal. `missing_end` keeps the reject coordinates at `none` / `0`; `terminal_defect` stays `missing_end` and `defect_source` stays `observation`.

Stdout versus stderr still follows the existing byte offset, including CRLF and the synthetic stderr scanner sentinel.

## Privacy bounds

Retained data is the closed tokens and integers above. The record does not contain raw JSON, the original stdout or stderr, prompts, message text, tool arguments or results, credentials, unknown key names, unknown type strings, or `encoding/json` error text. There is no digest and no raw-capture flag. The projection is not terminal acceptance evidence. `TerminalDefect`, subtype, held, fallback, and result text keep their previous roles.

## Checks

`GOCACHE=/tmp/gocache-cardex-mixed` was required in this session because the default Go build cache was not writable. Commands from the worktree:

- `go test -count=1 -timeout 300s -run 'TestGrokStreamRejectFirstLocusPrivacyAndBusinessBytes|TestGrokDiagnostics|TestGrokBuild105|TestNativeGrokLifecycleAndSyntheticMeta|TestBoardDisclosure|TestGrokBuildMalformed|TestGrokBuildTerminal|TestGrokBuildLegacy' .` — `ok` (5.670s).
- After the final privacy cases: `go test -count=1 -timeout 180s -run 'TestGrokStreamRejectFirstLocusPrivacyAndBusinessBytes|TestGrokDiagnosticsTaskEventReadback|TestNativeGrokLifecycleAndSyntheticMeta' .` and `go vet .` — `ok` (5.648s), vet clean.

The readback test persists `grok_diagnostics` on the task and the held/done event and compares those JSON objects. It covers a legal stream, malformed and typed failures, CRLF stderr boundary, blank-line-sensitive positions, scanner loss, post-end records, and a 40-key secret object whose stored unknown-key count is 32. Business results stay the previous strings: legal text is unchanged; missing end stays `Grok Build 流缺少终局 end 事件` with `ObservationComplete` still true; recognized incomplete and invalid-terminal messages are unchanged. Held status, attempt count, and fallback behavior in that test are unchanged.

## Author review

Separate pass after the implementation: read the parser diff for acceptance side effects, then ran fixtures whose type strings, key names, nested keys, values, malformed lines, and `encoding/json` syntax errors contain unique secrets.

Findings:

- Shape, end, plan, tool, usage, and command acceptance still call the original boolean predicates. Diagnosers run only after a predicate has already failed, or to describe an already-rejected decode.
- Unknown frames, missing fields, duplicate ends, tool closure, and scanner failure still fail closed. The `wasComplete` guard only stops a later record from becoming the first diagnostic locus.
- Existing `terminal_defect` / `defect_source` rows in the readback table are unchanged.
- Wire types `absent`, `none`, and `unknown` initially collided with diagnostic sentinels. They now normalize to `unknown`. The privacy test locks that.
- A canonical field token such as `data` or `message` can appear in `reject_field` when that allowlisted key is the shape problem. The value does not. Unknown names do not appear.

No further defect remained after that normalization fix and the final green run.

## No parser-policy change

Strict acceptance is still the original shape and terminal checks from 0.10.19. Subtype, `IsError`, `Result`, `ObservationComplete`, terminal counts, held, and fallback inputs are not derived from the new fields. Historical card `t0922-1832-a4cb` / attempt `at6a66d26af504825d` was not read, resumed, or reclassified. This diagnostic cannot recover its missing raw stdout.

## Build and candidate

```
go build -o bin/cardex .
```

For current checkouts after the source move, build with `go build -o bin/cardex ./cmd/cardex`. The command above records the original candidate build.

Candidate: `/Users/ottoprua/Projects/cardex-worktrees/mixed-routing-20260922/bin/cardex`

`bin/` is gitignored. This stage does not install, push, or change service config.

## Remaining local apply / canary gate

Parent applies this commit and runs one isolated read-only fixed-fixture provider canary with the candidate binary and the normal wrapper. The canary is the parent's authorized step. Do not treat `reject_*` as proof the stream was accepted. Do not push, release, or raise the 150 minute step timeout. Do not retry card `t0922-1832-a4cb`.
