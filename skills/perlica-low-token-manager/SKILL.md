---
name: perlica-low-token-manager
description: Reduce context and token use in long-lived Perlica or Cardex management conversations by restoring state from durable pointers, delegating ordinary execution to fresh workers, waking only on material events, and reporting compact state deltas. Use when an agent manages projects, components, cards, reviews, releases, or several execution sessions. This is a portable management-agent playbook, not a Writer or Reviewer execution skill.
---

# Perlica Low Token Manager

Treat this directory as a portable onboarding attachment for a management agent. Cardex is an execution surface the manager may use; do not inject this Skill into ordinary Cardex Writer, Reviewer, test, or release prompts.

## Keep the manager small

The manager owns only:

- objective, scope, priority, dependency order, and acceptance boundary;
- task decomposition and selection of an execution surface;
- decisions when evidence changes direction;
- consumption of material terminals and the final user-facing status.

Delegate ordinary substantial source work, long scans, model evaluations and release preparation to bounded workers. Keep simple review or a distinct adversarial self-review in the owning stage when sufficient; do not create another card merely for review. Five minutes is a cost guideline, not a hard stop. Explicit main-session audits and small mechanical edits may remain local when within authority and cheaper than delegation. Keep effects scoped and label the author's self-check honestly, never as independent review. Ordinary bounded work does not require a second reviewer card or a new card/permission for each ordinary fix.

Default ordinary implementation to one complete result within the existing scope, route and budget, including ordinary diagnosis/fix/test iterations and the selected review. This is not a waiver of explicitly required independence. Apply changed defaults at a safe handoff without rewriting an active frozen contract.

For Perlica/Cardex dispatch, use this configuration-consumption path: classify the task → current `cardex route -root ROOT` preview (one selector family) → normal `add` or workflow auto writer admission → read the frozen requested and resolved route.

Classify the task, then preview the current configuration with one selector family:

```text
cardex route -root ROOT -work-class development
```

or

```text
cardex route -root ROOT -complexity medium -category general
```

Use work-class, or complexity with category; do not mix both families in one preview.

Admit new work through the same resolver, then read the frozen requested and resolved route:

- Direct card: ordinary `cardex add` (inspect with `-dry-run`) covering diagnosis, code, checks, repairs, and the author's self-check.
- One-stage workflow: `cardex workflow init ... -writer-engine auto -reviewer-engine ACTUAL_RUNNER` (with `-work-class` or `-route-class` as needed). After a finished design, `cardex workflow fanout` creates independent direction cards and does not create acceptance; `cardex workflow accept` jointly creates one acceptance card only after every required direction is done.
- Native Goal directions: `cardex workflow goal-direction` and `goal-launch` admit or launch one native direction using the same resolver and freeze at admission. A Goal completion still requires the existing goal-sync and goal-level acceptance path. One native Goal session reaching done does not accept the whole goal.

Reviewer remains explicit: `-reviewer-engine` is not covered by the writer matrix. Explicit owner pins (runner, model, session, and explicit 4.6 pins) win. Freeze requested and actual source/model/effort. Config edits do not rewrite old held/running/Goal/terminal tasks or historical sessions. Fallback still requires its existing quota, zero-activity, custody, and identity evidence. Authentication failures, safety refusals, transient rate limits and unknown results are not quota exhaustion and do not authorize rerouting. Unavailable routes must be reported, not silently replaced.

If Cardex is unavailable and manual fallback is already authorized, use one fresh `MANUAL_LOCAL_GOVERNED_FALLBACK` worker after deduplication and custody recovery, preserving repository, owned paths, model route, attempt ceiling, acceptance boundary, callback, and receipt route. Otherwise ask for that authority. Label manual evidence honestly; do not relabel it as native Cardex or silently change providers. Do not move the work back into the long-lived manager conversation.

## Restore from durable state

On wake, reconstruct only this working set:

1. current objective and acceptance boundary;
2. active card or worker identifiers;
3. latest material terminal for each active lane;
4. current blockers and resource ownership;
5. exact next gate.

Prefer receipts, files, card state, and concise thread summaries over replaying full conversation history. Read older turns or raw logs only when an unresolved decision depends on them.

## Run an event-driven loop

1. Dispatch each bounded task once and record its identifier and callback destination.
2. End the management turn after dispatch when no decision remains.
3. Wake on a material terminal, an approval/decision request, a resource conflict, or new user input.
4. Use thread waits or callbacks for active work. Do not run shell `sleep` polling loops, repeatedly read unchanged tasks, or narrate unchanged progress.
5. Send component-local results directly to that component manager. Wake the top-level manager only for cross-component order, shared-resource conflicts, Owner decisions, or final portfolio status.

## Compress every callback

Report deltas, not history. Include only:

- stable coordinate and classification;
- highest proven layer: design, candidate, review, integrated, prelive, or live;
- what changed since the previous material event;
- card, commit, receipt, and digest pointers needed to verify it;
- first blocker or falsifier;
- one exact next gate and responsible manager.

Link large artifacts instead of pasting them. Do not paste raw model streams, full prompts, repeated test output, or entire prior packets unless the consumer must inspect those bytes.

Use this compact shape when practical:

```text
MATERIAL_DELTA_V1
coordinate=<stable coordinate>
classification=<state>
highest_layer=<layer>
changed=<short delta>
evidence=<card/commit/receipt/digest pointers>
blocker=<first blocker or none>
next_gate=<one action + owner>
```

## Preserve acceptance semantics

Keep these evidence facts distinct: design, candidate, review with its actual method, integration and actual runtime/user-path acceptance. Simple work may cover them in one short result rather than a sequence of separate cards or PRELIVE approvals. A running process, successful provider login, green focused tests, or a receipt alone does not prove later effects.

Treat unknown outcomes, missing or invalid terminals, incomplete custody, and changed source outside scope as held for that attempt and its downstream evidence. Do not splice or retroactively repair the terminal. HOLD does not cancel the valid objective: ordinary known-result fix/test loops stay with the component manager, and a distinct successor may be admitted after custody recovery within existing route, retry/successor, scope, and budget authority. Explicit no-retry, no-successor, single-attempt, and sealed-lineage limits remain binding; ask only for genuinely missing authority.

## Measure the optimization honestly

Keep management-session and execution-session usage separate. When measuring, record input, cached input, uncached input, output, reasoning, total, call count, and observation window. Preserve unavailable values as unknown rather than deriving false precision.

The desired outcome is fewer manager calls and less uncached manager input while useful execution moves to fresh Cardex or worker sessions. Do not optimize merely by hiding execution usage inside the management total.

## Avoid these regressions

- Do not keep a manager alive to babysit a running card.
- Do not create duplicate cards because a callback is late.
- Do not send every component event through the top-level manager.
- Do not make managers re-read full repository context already captured by a task packet.
- Do not add new schemas, gates, or audit layers solely to save tokens.
- Do not inject this Skill into Cardex execution prompts; workers should receive only their bounded task contract.
