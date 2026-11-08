package notify

import (
	"strings"
	"testing"
)

func TestResolveDedupesScopeFallbackAndInbox(t *testing.T) {
	msg := Message{
		Origin:    "codex:thread-0001",
		Fallback:  "file:box",
		Kind:      "freeze",
		Project:   "p",
		Objective: "obj",
		Dir:       "/work/p/src",
		Receivers: []Receiver{
			{ID: "same", Adapter: "codex", Target: "thread-0001", Enabled: true, Scope: Scope{All: true}},
			{ID: "scoped", Adapter: "file", Target: "notes", Enabled: true, Scope: Scope{Projects: []string{"p"}}, Events: []string{"freeze"}},
			{ID: "by-dir", Adapter: "lark", Target: "chat-1", Enabled: true, Scope: Scope{DirPrefixes: []string{"/work/p"}}},
			{ID: "filtered", Adapter: "lark", Target: "chat-2", Enabled: true, Scope: Scope{All: true}, Events: []string{"done"}},
			{ID: "empty-scope", Adapter: "codex", Target: "thread-9", Enabled: true},
			{ID: "off", Adapter: "codex", Target: "thread-8", Enabled: false, Scope: Scope{All: true}},
		},
	}
	hops, skips, problems := Resolve(msg)
	if len(skips) != 0 || len(problems) != 0 {
		t.Fatalf("skips %v problems %v", skips, problems)
	}
	got := make([]string, len(hops))
	for i, hop := range hops {
		got[i] = hop.Key + "=" + hop.Address
	}
	want := []string{
		"origin=codex:thread-0001",
		"receiver:by-dir=lark:chat-1",
		"receiver:scoped=file:notes",
		"fallback=inbox",
		"inbox=inbox",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("hops %v", got)
	}
	fallback := hops[len(hops)-2]
	if fallback.Kind != "file" || fallback.ID != "box" {
		t.Fatalf("file fallback %+v", fallback)
	}
}

func TestHermesSessionBeatsProfile(t *testing.T) {
	msg := Message{
		Origin: "hermes:profile-a/example-manager",
		Receivers: []Receiver{
			{ID: "profile-only", Adapter: "hermes", Target: "profile-a", Root: "/profile-root", Enabled: true},
			{ID: "session-hit", Adapter: "hermes", Target: "profile-a/example-manager", Root: "/session-root", Enabled: true},
		},
	}
	hops, skips, problems := Resolve(msg)
	if len(skips) != 0 || len(problems) != 0 {
		t.Fatalf("skips %v problems %v", skips, problems)
	}
	var origin Hop
	for _, hop := range hops {
		if hop.Key == "origin" {
			origin = hop
		}
	}
	if origin.Sub != "session-hit" || origin.Root != "/session-root" || origin.Address != "hermes:/session-root|session-hit" {
		t.Fatalf("origin %+v", origin)
	}
}

func TestHermesSkipWhenMissingOrNotUnique(t *testing.T) {
	cases := []struct {
		name      string
		receivers []Receiver
		sub       string
		wantSkip  string
		wantSub   string
	}{
		{
			name:     "none",
			wantSkip: SkipNoSubscription,
		},
		{
			name: "two-sessions",
			receivers: []Receiver{
				{ID: "one", Adapter: "hermes", Target: "profile-a/example-manager", Enabled: true},
				{ID: "two", Adapter: "hermes", Target: "profile-a/example-manager", Enabled: true},
			},
			wantSkip: SkipSessionAmbiguous,
		},
		{
			name: "two-profiles",
			receivers: []Receiver{
				{ID: "one", Adapter: "hermes", Target: "profile-a", Enabled: true},
				{ID: "two", Adapter: "hermes", Target: "profile-a", Enabled: true},
			},
			wantSkip: SkipProfileAmbiguous,
		},
		{
			name:    "explicit-id",
			sub:     "example-manager",
			wantSub: "example-manager",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := Message{
				Origin:         "hermes:profile-a/example-manager",
				SubscriptionID: tc.sub,
				Receivers:      tc.receivers,
			}
			hops, skips, problems := Resolve(msg)
			if len(problems) != 0 {
				t.Fatal(problems)
			}
			var origin *Hop
			for i := range hops {
				if hops[i].Key == "origin" {
					origin = &hops[i]
				}
				if hops[i].Kind == "hermes" && tc.wantSkip != "" {
					t.Fatalf("hermes hop %+v", hops[i])
				}
			}
			if tc.wantSkip != "" {
				if len(skips) != 1 || skips[0] != tc.wantSkip || origin != nil {
					t.Fatalf("skips %v origin %v", skips, origin)
				}
			} else if origin == nil || origin.Sub != tc.wantSub || origin.Root != "" {
				t.Fatalf("origin %+v", origin)
			}
			foundInbox := false
			for _, hop := range hops {
				if hop.Key == "inbox" {
					foundInbox = true
				}
			}
			if !foundInbox {
				t.Fatal("inbox missing")
			}
		})
	}
}

func TestEventsFilterStillDeliversOrigin(t *testing.T) {
	msg := Message{
		Origin: "codex:thread-0001",
		Kind:   "freeze",
		Receivers: []Receiver{{
			ID: "done-only", Adapter: "codex", Target: "thread-9", Enabled: true,
			Scope: Scope{All: true}, Events: []string{"done"},
		}},
	}
	hops, skips, problems := Resolve(msg)
	if len(skips) != 0 || len(problems) != 0 {
		t.Fatalf("skips %v problems %v", skips, problems)
	}
	if len(hops) != 2 || hops[0].Address != "codex:thread-0001" || hops[1].Kind != "inbox" {
		t.Fatalf("hops %+v", hops)
	}
}
