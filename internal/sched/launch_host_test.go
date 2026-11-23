package sched

import "testing"

func TestSchedHostAttemptLeavesTestsAndFakeChildren(t *testing.T) {
	env := func(key string) string {
		switch key {
		case "CARDEX_SCHED_HOST":
			return "1"
		case "CARDEX_SCHED_ATTEMPT":
			return "outer"
		default:
			return ""
		}
	}
	if id, ok := schedHostAttempt(env, []string{"cardex", "host", "a1"}); !ok || id != "a1" {
		t.Fatalf("host child %q %v", id, ok)
	}
	if _, ok := schedHostAttempt(env, []string{"pkg.test", "-test.timeout=10m"}); ok {
		t.Fatal("go test")
	}
	if _, ok := schedHostAttempt(env, []string{"pkg.test", "--test.v"}); ok {
		t.Fatal("long test flag")
	}
	fake := func(key string) string {
		if key == "CARDEX_FAKE_CHILD" {
			return "1"
		}
		return env(key)
	}
	if _, ok := schedHostAttempt(fake, []string{"cardex.test", "fake"}); ok {
		t.Fatal("fake child")
	}
	off := func(string) string { return "" }
	if _, ok := schedHostAttempt(off, []string{"cardex", "host", "a1"}); ok {
		t.Fatal("host flag off")
	}
}
