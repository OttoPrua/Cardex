package runner

import "github.com/OttoPrua/Cardex/internal/model"

// Lookup returns the executor for a channel runner name.
func Lookup(name string) (Runner, bool) {
	switch model.RunnerName(name) {
	case model.RunnerFake:
		return Fake{}, true
	case model.RunnerClaude:
		return Claude{}, true
	case model.RunnerCodex:
		return Codex{}, true
	case model.RunnerOpenCode:
		return OpenCode{}, true
	case model.RunnerGrok:
		return Grok{}, true
	case model.RunnerCursor:
		return Cursor{}, true
	case model.RunnerKimi:
		return Kimi{}, true
	case model.RunnerAGY:
		return AGY{}, true
	default:
		return nil, false
	}
}
