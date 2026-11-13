package runner

import "errors"

// GoalRunner is a runner with a native goal mode. The host turns it on for a
// card that requires goal_mode: ForGoal returns the configured copy, which
// needs a PTY and drives the CLI's own /goal command. GoalFacts is what the
// host records next to the process-exit fact after Wait.
type GoalRunner interface {
	Runner
	ForGoal(launch GoalLaunch) Runner
}

// GoalLaunch is one goal-mode launch.
type GoalLaunch struct {
	Prompt    string
	WorkDir   string
	AttemptID string
}

// GoalFacts names the goal text and the session directory a goal run watched.
type GoalFacts struct {
	Prompt     string
	SessionDir string
}

// GoalReporter is a configured goal runner that can report its facts.
type GoalReporter interface {
	GoalFacts() GoalFacts
}

// Shared goal terminals. Completion is a nil error from Drive.
var (
	// ErrGoalBudgetLimited means the goal stopped on its own budget. It is
	// not completion.
	ErrGoalBudgetLimited = errors.New("goal budget_limited")
	// ErrGoalBlocked means the CLI marked the goal blocked for a reason other
	// than its budget.
	ErrGoalBlocked = errors.New("goal blocked")
	// ErrGoalPaused means the CLI paused the goal (runtime or provider error).
	// Nobody is attached to resume it, so the run ends.
	ErrGoalPaused = errors.New("goal paused")
)
