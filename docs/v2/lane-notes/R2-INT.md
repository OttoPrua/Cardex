# R2-INT

## 做了什么
按顺序用 `git merge --no-ff` 合入 `v2-R2-C`，再合入 `v2-R2-D`。两处冲突都只在 `internal/e2e/scenarios_test.go` 的 import，并集保留 classify、harvest、host、quota、route。`TestS06` 仍把不完整事件流、工作区改动和通过的验收收成 passed。`TestS17` 仍按 pace 排序、红线排除、显式渠道绕过红线。`TestS18` 仍写入分级成功的 tier，超时退回规则 tier。`TestS24` 仍拒绝 worktree 外写入，只在非 darwin 上 skip。S01–S05、S07–S16、S19–S23 保持原 skip。

## 没做什么，以及原因
未合入 `v2-R2-B`。未改 `docs/v2/goals.md` 与 `docs/v2/design.md`。build、vet、test 一次通过，没有为红测改调用点。

## 接口请求
无。

## 已知问题
无。

## 修复记录
按顺序 no-ff 合入时，冲突保留双方已落地场景，不用后合入的 skip 盖掉先合入的实现。 → internal/e2e/scenarios_test.go:14, internal/e2e/scenarios_test.go:74, internal/e2e/scenarios_test.go:173, internal/e2e/scenarios_test.go:182, internal/e2e/scenarios_test.go:189, internal/e2e/scenarios_test.go:204, internal/e2e/scenarios_test.go:224, internal/e2e/scenarios_test.go:266 → TestS06, TestS17, TestS18, TestS24
本 worktree 的 build、vet、test 必须通过且改动已提交。 → internal/e2e/scenarios_test.go:74, internal/e2e/scenarios_test.go:173, internal/e2e/scenarios_test.go:204, internal/e2e/scenarios_test.go:325 → TestS06, TestS17, TestS18, TestS24
