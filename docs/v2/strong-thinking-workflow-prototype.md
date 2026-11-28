# Strong-thinking workflow prototype (ST2)

This branch adds a controller-side state machine and an actual synthetic
integration fixture. It does not add a CLI route, run normal cards, enable real
models, invoke T0, make network calls, access an account, relax sandbox
defaults, or publish provider output. Real execution and the cross-family
review/change gate are outside ST2. The last reachable state is
`st3_required`, not "pass", "ready to publish" or "complete".

## Preview and explicit approval

A plan is canonical JSON with a digest, revision, template version, exact task
SHA, scope, project, pinned evidence commit/paths/digest, ordered per-file digest/size manifest resolved from the exact bare Git
storage format and object ID, slot-specific allowed
routes and token ceilings, and a global budget. The plan expands to stable
slots with immutable stage/seat/round dependencies. A new plan cannot inherit
the old approval. Idempotent replay of the same explicit approval is safe; an
approval receipt is not usable for a different plan. Spend carries over to a
revised run. A canceled plan with a genuinely unresolved launch retains its
hold and cannot evade it via a new revision. The authenticated owner/controller
must supply the approval identity; this package is not a public auth surface.

The strong fixture exercises optional framing, fixed evidence, two blind seats,
one peer round, optional authorized second peer round, optional targeted
verification, and synthesis. The second round needs a recorded new objection
from a valid peer report, and targeted verification needs a verified dispute.
The anonymous issue text, not the actor/identity, is frozen into the exact
bundle. The light fixture runs the same evidence standard and one answer, then
stops before ST3. It does not implement automatic cross-run evidence reuse.

## Durable launch and budget

- Stage dependencies require independently verified accepted ST1 artifact
  envelopes and strict structured phase receipts. An ordinary passed attempt
  is not readiness. A v1 receipt uses the exact result heading followed by
  `Phase: ...`, contiguous structured fields, and an optional blank-line
  boundary; later prose cannot become a control. Evidence/answer/verification
  receipts explicitly bind the independently approved evidence fingerprint. Only evidence and targeted
  verification receive the approved raw bytes, which are pinned by ST1 bundle
  custody. The Fake evidence child reads its actual input file to produce the
  fixture facts, not merely the script's digest. Semantic factual review of
  real output is not established.
- One SQLite writer reserves each slot's max (including parallel slots) before
  any synthetic dispatch. The intent pins exact stage, actual route, input and
  custody. Launch claim and final start authorization are one time and committed
  before returning, not claims of exactly-once external processes. The final
  edge rechecks the live custody descriptor, staged bundle bytes, pending
  attempt, approved route and phase; the Fake child independently opens and
  hashes its exact file-transport input before running. The helper uses fixed
  argv/env; it checks the disk fixture exactly once, passes bounded script
  bytes by stdin, and suppresses path diagnostics in the receipt channel.
  Inline/env overrides and control characters in paths are refused.
  A recovered
  uncertain launch is stopped rather than repeated. A durable matching
  terminal attempt and sealed artifact can be reconciled without a new call.
  A lost prelaunch custody handle also blocks; it is not recreated.
- Only the existing local Fake adapter is eligible in the synthetic path. A
  separate confirmation receipt records one exact T0 intent only; an ST2
  dispatch is prohibited even with that receipt. Real/unknown adapters,
  cancellation, missing/malformed evidence, and unverified/over-limit usage
  do not advance subsequent stages.
- Reservations are not a live provider hard token cutoff. The fixture accounts
  observed exact Fake usage once from an authenticated local child receipt,
  rejects duplicate/malformed/conflicting markers, records overflow and stops;
  unknown usage holds the reservation. If a parallel failure or cancellation
  has already stopped the run, a valid outstanding receipt records spend
  without accepting output or unblocking dependencies. Actual route and model/family receipts are checked
  against the plan. Any future real provider instrumentation must independently
  establish what it can verify.

## Migration status

Schema v16 is a **prototype reservation** only. Test fixtures explicitly install
these internal tables in temporary SQLite databases after verifying the exact
typed T1 v15 activation. Ordinary `Open` and the scheduler do not install or
use them; protocol installation deliberately leaves the global v15 stamp and
legacy on-disk tier values intact. Before release, the actual public T1 v15
migration must be present, followed by a tested additive v16 migration. The
prototype must not mark a real database v16 or skip v15.

## Reproduction

`go test ./internal/store ./internal/prompt ./internal/e2e -run '^TestStrong'`

The end-to-end test stages actual synthetic Fake input by file path, executes
its existing child, seals and harvests results through ST1, and verifies the
stage ledger after restart. The temporary local bare Git fixture is evidence
selection, not proof of host/network isolation; no production runner is
approved here.
