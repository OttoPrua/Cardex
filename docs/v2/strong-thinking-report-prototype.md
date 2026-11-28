# Research workflow reports (ST4 synthetic prototype)

Inspect an exact saved synthetic run revision in a stable, checkpointed CardeX home or prepared copy on Linux:

```sh
cardex --home /path/to/existing-synthetic-home research report \
  --run RUN --revision 1 --sha PLAN_SHA
```

This output is **private**. It includes plan and artifact fingerprints, exact
attempt attribution when verified, per-stage status and blocker, review and
rewrite receipts including dissent, and an independent reconciliation against
the persisted reservation/spend totals. Missing values are unknown, not zero.
An attempt row, authenticated local Fake dispatch (even if its token telemetry
is unknown), provider/model-internal
call, and tool call mean different things. Only the authenticated Fake child
receipt supplies measured synthetic tokens; estimates and provider telemetry
stay in separate columns. Current runs have one attempt slot per stage and no
classification or automatic retries. Retry notices are observed events, not
a count of dispatched retries; limited event metadata is displayed without
event payloads.

Create a new offline file using the narrower export:

```sh
cardex --home /path/to/existing-synthetic-home research report \
  --run RUN --revision 1 --sha PLAN_SHA --redacted > ./research-redacted.json
```

The export contains a fixed task category and generic sanitized task summary,
flow/version, scope-limited conclusion, aggregate known synthetic usage and
unknown count, validation basis and limitations. Its SHA-256 identifies the
compact JSON object under `report`, not the CLI's pretty-printed outer
envelope or the source artifacts. Do not publish the private report. Review
the redacted file separately before any external use; no publishing,
server route, website rendering, or network action is included. The command
does not initialize a home, install or migrate schema, read runtime/credential
configuration, dispatch work, or alter the database. The same board read-only
inspection boundary is reused from O1: pinned descriptor, no symlinks, no live WAL/SHM
or journal, and no unsafe reader fallback on unsupported hosts. There is no
page/UI added by this change.

A public synthetic verification recipe is
`go test -count=1 -run '^TestStrong(RunReport|ResearchReport|RedactedReport)' ./internal/store ./internal/board ./internal/e2e ./cmd/cardex`.
The light fixture settles 7 + 7 + 11 + 13 = 38 synthetic tokens, separately
retains major dissent and a rejected same-family route, and verifies that a
tampered sealed result lowers the conclusion rather than emitting the corrupt
text. Separate cases exercise measured failed work, unknown use, exact
revision rejection, and canaries in stored results, paths, identities, prompts,
and raw IDs. These verify the synthetic workflow and disclosure policy, not
real-provider quality, hard isolation, or reproducibility of private inputs.
