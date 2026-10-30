package runner

import "cardex/internal/model"

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
	default:
		return nil, false
	}
}
