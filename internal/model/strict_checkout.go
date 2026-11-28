package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// StrictNoExecutorEvent is written by the exact host only after a proven
// no-process Start failure. A killed or crashed executor is not equivalent.
const StrictNoExecutorEvent = "strict_executor_never_started"

// StrictCheckoutID attributes a receipt to a particular host claim and branch
// checkout without copying filesystem paths to the event.
func (a Attempt) StrictCheckoutID() string {
	v := struct {
		ID, Card, Worktree, Branch, Base, Started string
		Index, PID                                int
	}{a.ID, a.Card, a.Worktree, a.Branch, a.BaseCommit, a.StartedAt.UTC().Format(time.RFC3339Nano), a.Index, a.PID}
	raw, _ := json.Marshal(v) // strings and ints only
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
