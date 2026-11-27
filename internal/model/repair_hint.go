package model

// EventRepairHint contains an enum, never operator text or archive material.
const EventRepairHint = "repair_hint"

type RepairHint string

const (
	RepairHintBoundary RepairHint = "boundary-check"
	RepairHintRerun    RepairHint = "rerun-checks"
	RepairHintConflict RepairHint = "resolve-conflicts"
)

// PromptText returns a fixed, non-sensitive action for the executor. Unknown
// codes, including legacy free-form notes, cannot be rendered.
func (h RepairHint) PromptText() string {
	switch h {
	case RepairHintBoundary:
		return "补上边界检查"
	case RepairHintRerun:
		return "重跑验收检查"
	case RepairHintConflict:
		return "解决合并冲突"
	default:
		return ""
	}
}
