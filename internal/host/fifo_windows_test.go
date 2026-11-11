//go:build windows

package host_test

import "testing"

func makeBlockingPrompt(t *testing.T, path string) func() {
	t.Helper()
	t.Skip("needs a FIFO prompt; Windows has no mkfifo")
	return func() {}
}
