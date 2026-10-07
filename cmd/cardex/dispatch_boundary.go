package main

import "fmt"

const (
	workModeDirect = "direct"
	workModeStaged = "staged"
	workModeGoal   = "goal"
)

// ConfirmedDispatchPolicy is an explicit owner opt-in for one saved shape.
// It never blocks an ordinary direct submission.
type ConfirmedDispatchPolicy struct {
	OnboardingDecision string `json:"onboarding_decision,omitempty"` // adopt | adjust | decline
	WorkMode           string `json:"work_mode,omitempty"`           // direct | staged | goal
}

// DispatchDecision is the shape actually written on a card. Explicit owner
// direct submission wins over a saved goal policy.
type DispatchDecision struct {
	WorkMode string `json:"work_mode,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// ordinaryDirectDecision admits a direct card. A saved Goal policy or
// onboarding opt-in is recorded in the reason and does not reject the card.
// Explicit staged or goal modes name cardex workflow and write nothing.
func ordinaryDirectDecision(cfg *Config, explicitWorkMode string) (*DispatchDecision, error) {
	mode := explicitWorkMode
	switch mode {
	case "", workModeDirect:
	case workModeStaged, workModeGoal:
		return nil, fmt.Errorf("work mode %q is manager-organized; use cardex workflow; 未写入本卡", mode)
	default:
		return nil, fmt.Errorf("unknown work mode %q; use cardex workflow; 未写入本卡", mode)
	}
	savedGoal := cfg != nil && cfg.ConfirmedDispatch != nil && cfg.ConfirmedDispatch.WorkMode == workModeGoal
	optIn := cfg != nil && cfg.ConfirmedDispatch != nil && cfg.ConfirmedDispatch.OnboardingDecision != ""
	if mode == "" && !savedGoal && !optIn {
		return nil, nil
	}
	reason := "ordinary add is direct"
	if savedGoal {
		reason = "ordinary add is direct, not native Goal"
	}
	if optIn && savedGoal {
		reason = "ordinary add is direct, not native Goal; onboarding opt-in does not block direct submission"
	} else if optIn {
		reason = "ordinary add is direct; onboarding opt-in does not block direct submission"
	}
	return &DispatchDecision{WorkMode: workModeDirect, Reason: reason}, nil
}
