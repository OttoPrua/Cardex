# Stage result: admission repairs and one composed-acceptance cycle

Branch `codex/goal-admission-composition-20261019` (base `e908173ee48648bcd67038652c202e298c4b7263`). `git log -1 --stat` / `git status` captured in `commit.txt` (retained baseline remains dirty/untracked).

## Diff paths

This stage (intended sources):

- `cmd/cardex/runner.go` — JSON presence/type of `p0`/`p1` on `parseReviewVerdict`; `advancePlannedReview` rejects incomplete JSON pass via `reviewVerdictIsAdmissiblePass` before `appendCompletedReview`; Sol release uses the same predicate
- `cmd/cardex/workflow_gate.go` — admissible pass; incomplete arrays are `incomplete_evidence`
- `cmd/cardex/task.go` — `scanTasks` vs display `loadTasks`
- `cmd/cardex/multilane_runtime.go` — unreadable task evidence on reconstructed live writers
- `cmd/cardex/workflow_acceptance.go` — fanout per-direction claims; `goal-launch` shares `goal-run` admission
- `cmd/cardex/workflow_goal.go` — shared concurrency/DAG/session/capacity predicate
- `cmd/cardex/workflow_test.go` — Go-zero pass is not JSON presence
- `cmd/cardex/goal_admission_composition_test.go` — counterexamples, composed-candidate CLI loop, and `TestAdvancePlannedReviewRejectsIncompleteJSONPass` driving `advancePlannedReview` / `postComplete`
- `skills/cardex-dispatch/SKILL.md`, `docs/workflows.md`, `docs/workflows.en.md`, `docs/dispatch-agent.md`, `docs/getting-started.md`

Retained baseline (not this stage; left dirty/untracked):

- `cmd/cardex/attempt_control.go` (native canceled-attempt binding)
- `cmd/cardex/tshare_cancel_closeout_test.go`
- `docs/handoffs/cardex-rdw-critical-review-20261008.md`

`docs/getting-started.en.md` is listed in the owned-path set but is absent from this worktree; it was not invented here.

`workflow_gate.go` remains the ReviewOutput consumer (`writeReviewOutput` / `loadTaskResultForGate` / `evaluateIntegrationRelease`). No second result-file system.

## Commands and results

Scratch: implementer dir named by the goal harness. Outputs copies: `.cardex-stage/outputs/`.

```
GOCACHE=$WORKTREE/.cache/go-build GOTMPDIR=$WORKTREE/.cache/go-tmp

go test -count=2 -timeout 300s -run 'TestParseReviewVerdictAdmissiblePassJSONContract|TestAdvancePlannedReviewRejectsIncompleteJSONPass|TestReviewVerdictAdmissiblePassVocabulary|TestMalformedTaskRecordDisplayVsAdmission|TestGoalLaunchReusesGoalRunAdmission|TestFanoutDirectionIdentitiesAndOverlap|TestErroneousAcceptanceAndCoverage|TestTwoDirectionComposedCandidateConsumerLoop|TestWorkflowFanoutAcceptAndRoundCommands' ./cmd/cardex
# -> PASS ok cardex/cmd/cardex 27.475s
# captured: focused-tests.log

go test -count=1 -timeout 180s -run 'TestOrdinaryDirectIgnoresGoalPolicyAndAdmissionIsNotLaunch|TestWorkflowFanoutAcceptAndRoundCommands|TestNativeReviewConsumerUsesOnlyFinalConclusion|TestNativeReviewArtifactRefusesMissingStaleMismatchedAndUnreadable|TestGoalRunDisjointOverlapAndMissingPredecessor' ./cmd/cardex
# -> PASS ok cardex/cmd/cardex 6.280s
# captured: baseline-groups.log

go build -o $SCRATCH/cardex ./cmd/cardex
# SHA256 703e7a39121a8b90be95b26fb2400e78f25cde2ed36242612ceb917272726716
# sidecar: .cardex-stage/outputs/cardex.sha256

CARDEX_CANDIDATE_BIN=$SCRATCH/cardex go test -count=1 -v -run TestTwoDirectionComposedCandidateConsumerLoop ./cmd/cardex
# twice, both PASS; captured compose-loop-1.log compose-loop-2.log
```

Also: `TestAdvancePlannedReviewRejectsIncompleteJSONPass` plus `TestFinalOwnerSerialBackendReviewPlan`, `TestP1KimiContradictoryPassProducesDurableDisagreementEscalation`, `TestHandleReviewVerdictRejectsPassWithOpenFindings`, `TestFinalOwnerOrdinaryBackendKimiAdversarialReviewCreatesSeparatelyRoutedRepair` at `-count=2` PASS (4.172s).

## Two-direction composed candidate (built CLI)

Each consumer-loop run execs the built binary for `workflow init|fanout|freeze-candidate|accept|accept -complete`. Direction file writes, `git add`/`git commit`, and `go test ./internal/billing` on the composed tree are fixture/manual.

Loop 1 (compose-loop-1.log), same observables both inner rounds (`acceptance_created=1` then `acceptance_created=0 existing=...`, `goal_accepted=false` until `-complete` with a JSON empty-array pass):

- round 1: alpha=`t1019-0510-8d29` beta=`t1019-0510-34c9` commit=`e1b267c6d61e8198ffa44764b2fae6526c328dc5` tree=`08c9c82994bdeefa21b9ac3995c1ae06efe5686e` acceptance=`t1019-0510-37ac`
- round 2: alpha=`t1019-0510-ba52` beta=`t1019-0510-7356` commit=`7433f0501ac580a986a50da135c1848aa7e7f305` tree=`08c9c82994bdeefa21b9ac3995c1ae06efe5686e` acceptance=`t1019-0510-71a5`

Loop 2 (compose-loop-2.log):

- round 1: alpha=`t1019-0510-6df8` beta=`t1019-0510-d24b` commit=`8d5bb890f9593bf95c3577dca99a03bc9c0c335c` tree=`08c9c82994bdeefa21b9ac3995c1ae06efe5686e` acceptance=`t1019-0510-8680`
- round 2: alpha=`t1019-0510-2974` beta=`t1019-0510-1e3d` commit=`6ed2cac34cca4af0416eeac0249395243b18fd02` tree=`08c9c82994bdeefa21b9ac3995c1ae06efe5686e` acceptance=`t1019-0510-566f`

Composed tree SHA stays `08c9c82994bdeefa21b9ac3995c1ae06efe5686e` (both directions' interface: `auth.Token` consumed by `billing.Charge`). Isolated fake-provider temp roots only; no `~/.cardex`, no installed binary, no real model.

## Implemented vs manual

Automatic (CLI): `workflow init`, `fanout` (including `-direction-write-paths`), `freeze-candidate`, `accept`, `accept -complete`, `goal-direction`, `goal-launch` / `goal-run` admission, `list` warn-and-skip of a corrupt sibling, tick/admit/launch fail-closed or scoped constraint, owner planned-review closeout of incomplete JSON pass.

Manual: Git composition of the two direction diffs, fixture `go test` of the composed tree, planting a fake-provider review transcript on the acceptance card.

Not automatic multi-round operation. `goal-round` remains an explicit command.

## Author adversarial review (changed consumer paths)

Findings fixed in this attempt:

1. Incomplete `verdict=pass` (`missing` / `null` / wrong-type `p0`/`p1`) was admissible because `len(nil)==0`. Parser now requires JSON array presence; `acceptanceAdmittedLocked`, `evaluateIntegrationRelease`, and `handleReviewVerdict` consume that parse. A Go-zero struct is not presence.
2. `advancePlannedReview` still treated a parsed `{"verdict":"pass"}` as a closed pass (`len(P0)==0` on missing arrays), called `appendCompletedReview`, and on a last-stage Sol/Kimi gate marked the owner-review root `statusDone`. It now calls `reviewVerdictIsAdmissiblePass` before `appendCompletedReview`; missing/null/wrong-type pass holds with `incomplete_review_evidence` and leaves the Sol/Kimi stage pending. Present non-empty findings still take the existing Kimi disagreement / Sol-hold paths. `TestAdvancePlannedReviewRejectsIncompleteJSONPass` drives `advancePlannedReview` and the `postComplete` caller.
3. `loadTasks` skipped corrupt siblings, so tick/admit/launch treated them as absence. Display still skips with a warning. Admission uses `scanTasks` + reconstructed claims. Independent proof (workflow writer binding, attempt workspace lease) constrains only that scope; otherwise an unknown-scope poison fails closed. Fanout refuses when any sibling task file is unreadable.
4. `goal-launch` used a global unreleased-attempt busy flag. It now shares `goalLaunchConcurrencyBlocked` with `goal-run`: write-domain/resource conflict, DAG, reused session, capacity. Disjoint proven directions launch within `max_parallel`.
5. Counting reconstructed live writers toward `max_parallel` treated an integration card that shares the writer's workspace lease as a second slot and refused a disjoint second launch at `max_parallel=2`. Slot counting uses live attempts/procs (plus live attempts on unreadable IDs). Conflict, not capacity, handles unknown-scope poisons.
6. Fanout copied one domain ID/lineage onto every name. `-direction-write-paths` / `-direction-write-resources` mint independent identities for explicit disjoint scope. Omitted scope still copies the workflow claim (paths/resources/ID), so overlap protection is not narrowed.

Remaining (not silent pass):

- A damaged writer that holds only a workspace lease and has no attempt record is constrained on overlap if scope can be recovered, but is not counted as a capacity slot.
- Same-card adapter resume is already supported. No additional Codex-compatible continuation was demonstrated on this path. Inter-card and cross-provider SessionID reuse remain unsupported. No extra Codex simplification item was established; stages 1–2 are still complete.

## Context-plan items

Reused: design/fanout/accept/candidate coverage, write-domain/DAG/attempt/lease, ReviewOutput binding, persistent design/manager sessions, same-card resume.

Fixed: four confirmed admission defects; owner planned-review incomplete JSON pass; one real two-direction composed-candidate fake-consumer cycle; docs/skill boundaries.

Deferred: stage 3 loop, ten-role profiles, model scoring, extra ledger, glob write-domain system, automatic worktree cleanup, new approval layer, second result-file system, global Codex app settings, `getting-started.en.md` (file missing).

## Remaining uncertainty

Isolated fake-provider roots are the bar; this did not exercise the real `~/.cardex` board. Git composition in the fixture is labeled manual. Production `max_parallel` (24) is unchanged.
