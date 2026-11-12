package host

import "time"

// SetResultPersistTimeoutForTest replaces the post-execution step bound for
// this process. The production default stays 30s unless a test calls this.
func SetResultPersistTimeoutForTest(d time.Duration) func() {
	prev := resultPersistTimeout
	resultPersistTimeout = d
	return func() { resultPersistTimeout = prev }
}

func SetCancelPollForTest(d time.Duration) func() {
	old := cancelPoll
	cancelPoll = d
	return func() { cancelPoll = old }
}
