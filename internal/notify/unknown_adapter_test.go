package notify

import (
	"errors"
	"testing"
)

func TestResolveUnknownOriginAdapterKeepsInbox(t *testing.T) {
	hops, _, problems := Resolve(Message{Origin: "grokbot:abc", Fallback: "cursor:x"})
	if len(hops) != 1 || hops[0].Key != "inbox" {
		t.Fatalf("hops %+v", hops)
	}
	var unknown *UnknownAdapterError
	if len(problems) != 2 || !errors.As(problems[0], &unknown) || unknown.Kind != "grokbot" || unknown.Key != "origin" {
		t.Fatalf("problems %v", problems)
	}
}
