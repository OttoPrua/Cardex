package model

import (
	"fmt"
	"strings"
)

// ExecutionDependencyIDs excludes the first dependency when it names the
// subject or source of an operation rather than a completion gate.
func (c Card) ExecutionDependencyIDs() []string {
	subject := ""
	switch c.Kind {
	case KindReview, KindRepair, KindRebase, KindNormalize:
		if len(c.Deps) > 0 {
			subject = strings.TrimSpace(c.Deps[0])
		}
	}
	var ids []string
	for _, id := range c.Deps {
		id = strings.TrimSpace(id)
		if id != "" && id != subject {
			ids = append(ids, id)
		}
	}
	return ids
}

// DependencyBlocker explains why an execution dependency is not satisfied.
// dep is the current stored card, or the zero value when it does not exist.
func (c Card) DependencyBlocker(id string, dep Card) string {
	// A review can reach done after rework, blocked, or escalation. Neither a
	// terminal status nor historical verdicts prove current approval of its
	// subject. Gate on the subject instead until exact review receipts exist.
	if c.StrictDeps && dep.Kind == KindReview {
		return fmt.Sprintf("strict dependency %s is a review card; depend on the merged subject instead", id)
	}
	switch dep.Status {
	case CardDone, CardMerged:
		return ""
	case CardCanceled:
		if !c.StrictDeps {
			return ""
		}
	case "":
		if c.StrictDeps {
			return fmt.Sprintf("strict dependency %s does not exist", id)
		}
		return fmt.Sprintf("dependency %s does not exist", id)
	}
	if c.StrictDeps {
		return fmt.Sprintf("strict dependency %s is %s; requires done or merged", id, dep.Status)
	}
	return fmt.Sprintf("dependency %s is %s", id, dep.Status)
}
