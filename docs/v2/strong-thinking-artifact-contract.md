# Strong-thinking artifact and input contract (ST1)

This change provides internal storage and preflight primitives only. It does not
enable strong thinking, implement approvals, advance stages, call a provider, or
modify a normal card, channel, account, sandbox default or runtime. A trusted
controller must later supply the independently approved plan and stage identity.

- Before an isolated attempt, the controller preregisters the pending attempt's
  exact run/stage/role/seat/card/workspace in SQLite. Already started or passed
  ordinary attempts cannot be attached. A prelaunch handle pins the new, empty
  output slots to a workspace descriptor. After the process tree stops, opt-in
  harvest seals the canonical Markdown and optional structured reference files
  before acceptance commands run, not summarized logs or display evidence.
  V1 Markdown must start with the explicit conclusion heading and contain a
  body. Only a zero-exit successful ledger attempt yields a stored artifact.
  Larger input is rejected and never silently truncated.
- Local custody uses a pinned directory tree, relative no-follow nonblocking
  opens, positive regular-file and single-hardlink checks, and before/after
  file and workspace identity checks. Linux and Darwin have implementations;
  unsupported platforms deny capture. This is file custody, not runner
  filesystem or network isolation.
- Store verifies the actual final attempt ledger and persists run, source stage,
  exact producer, canonical origin, format, byte count, SHA-256 and original
  bytes. The binding pins a separately verified envelope over both body and
  structured-reference digests, the ordered input manifest and rendered bundle
  fingerprints, and the unique committed custody identity. Producer registration and target allowlists are immutable; subsequent
  reads verify that scope, size, fingerprint and attempt state again. A missing
  or inconsistent record produces no input. Normal evidence remains unchanged.
- An approved task digest pins one task per run. Both blind seats receive identical
  bytes, without upstream material or metadata about the seat. Each peer can
  read only the explicitly bound other blind artifacts, not its own or other
  reviews. Aliases are local to a recipient, stored with the input allowlist in
  the controller database, and no alias-to-real-identity map is exported. Before
  launch one independently committed capture claim checks the controller's
  original approved ordered allowlist digest (canonical empty list for blind
  stages) against storage, then pins it and the exact rendered bundle SHA. Equal
  prose does not make distinct source identities interchangeable. After the claim, bindings cannot be written. The runner can
  receive a copy of those exact bytes for file transport. Exact
  private key tokens already known from the run are rejected in outbound text;
  writing style or clues in prose may still be attributable.
- Optional peer citations are a separate `.cardex-strong-references.json` array
  of supplied alias labels. Synthesis rewrites only these structured refs to
  its own approved aliases. Missing dependencies stop assembly; Markdown content
  is preserved, including its own A/B numbering and literal labels. Arbitrary
  Markdown, a prior conversation, and a runner-supplied reference are never
  interpreted as authority to add inputs.
- Evidence reads use only an allowlist of exact regular blob paths on one full
  commit from a trusted, controller-owned local bare object store, not an active
  checkout. A returned file has its own content SHA-256. This protects evidence
  selection; it cannot prove a runner is isolated.

## Runner status

All existing runner adapters are ineligible for ST1 material. No production
strong argv version, fresh-session controls, external read boundary, or network
boundary have been verified for them. In particular, a macOS read-only evidence
snapshot does not enforce external reads or network isolation. A plan requesting
hard isolation is denied before input staging or process start. There is no fallback
to argv text, ordinary prompt construction, ambient session access or a weaker
sandbox. A synthetic subprocess exercises bounded path-only file transport; it
is not a test or assertion of production hard isolation.

## Verification

Use a synthetic Git repository, synthetic attempt ledger and local subprocess
only. Focused checks:
`go test ./internal/store ./internal/harvest ./internal/prompt ./internal/runner ./internal/strongcustody`.
