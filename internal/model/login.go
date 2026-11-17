package model

// LoginCommand is how a user signs a runner's CLI in, for messages that tell
// them what to do after an auth failure. Unknown runners return "".
func LoginCommand(r RunnerName) string {
	switch r {
	case RunnerGrok:
		return "grok login"
	case RunnerCodex:
		return "codex login"
	case RunnerClaude:
		return "claude, then /login"
	case RunnerCursor:
		return "cursor-agent login"
	case RunnerKimi:
		return "kimi, then /login"
	case RunnerAGY:
		return "agy, then sign in"
	case RunnerOpenCode:
		return "opencode auth login"
	}
	return ""
}
