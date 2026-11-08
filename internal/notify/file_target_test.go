package notify

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFileReceiverDeliveryWritesTarget(t *testing.T) {
	const text = "notice for the file receiver"
	cases := []struct {
		name      string
		same      bool
		wantFiles int
		wantHops  int
	}{
		{name: "one configured path and the home inbox", wantFiles: 1, wantHops: 1},
		{name: "two configured paths", wantFiles: 2, wantHops: 2},
		{name: "one shared path is one write", same: true, wantFiles: 2, wantHops: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			dir := t.TempDir()
			paths := []string{
				filepath.Join(dir, "a", "example-notice.txt"),
				filepath.Join(dir, "b", "example-notice.txt"),
			}
			if tc.wantFiles == 1 {
				paths = paths[:1]
			}
			if tc.same {
				paths[1] = paths[0]
			}
			ids := []string{"file-a", "file-b"}
			receivers := make([]Receiver, len(paths))
			for i, path := range paths {
				receivers[i] = Receiver{
					ID:      ids[i],
					Adapter: "file",
					Target:  path,
					Enabled: true,
					Scope:   Scope{All: true},
				}
			}
			msg := Message{
				QuestionID: "q-file-target",
				Objective:  "obj",
				Text:       text,
				Home:       home,
				Receivers:  receivers,
			}
			hops, skips, problems := Resolve(msg)
			if len(skips) != 0 || len(problems) != 0 {
				t.Fatalf("skips %v problems %v", skips, problems)
			}
			fileHops := 0
			inboxHops := 0
			for _, hop := range hops {
				if hop.Kind == "file" && strings.HasPrefix(hop.Key, "receiver:") {
					fileHops++
				}
				if hop.Kind == "inbox" {
					inboxHops++
				}
			}
			if fileHops != tc.wantHops || inboxHops != 1 {
				t.Fatalf("file hops %d inbox hops %d from %+v", fileHops, inboxHops, hops)
			}
			var inboxN int
			orig := inboxWrite
			t.Cleanup(func() { inboxWrite = orig })
			inboxWrite = func(name string, data []byte, perm os.FileMode) error {
				inboxN++
				return orig(name, data, perm)
			}
			svc := New(func(context.Context, []string) error {
				t.Fatal("file receiver executed a command")
				return nil
			}, nil)
			if err := svc.Deliver(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			if inboxN != 1 {
				t.Fatalf("inbox writes %d", inboxN)
			}
			inboxBody, err := os.ReadFile(inboxPath(home, msg.QuestionID))
			if err != nil || strings.Count(string(inboxBody), text) != 1 {
				t.Fatalf("inbox %v %s", err, inboxBody)
			}
			seen := map[string]bool{}
			for _, path := range paths {
				if seen[path] {
					continue
				}
				seen[path] = true
				if path == inboxPath(home, msg.QuestionID) {
					t.Fatalf("target is the home inbox %s", path)
				}
				body, err := os.ReadFile(path)
				if err != nil || strings.Count(string(body), text) != 1 {
					t.Fatalf("target %s %v %s", path, err, body)
				}
			}
		})
	}
}

// TestFileDedupWritesReceiverAndInbox checks that a file origin or fallback
// which string-equals file: plus a receiver target still writes that target.
// Those hops land in the home inbox, so they must not consume the receiver.
func TestFileDedupWritesReceiverAndInbox(t *testing.T) {
	const text = "notice that must reach the configured file"
	cases := []struct {
		name     string
		origin   func(target string) string
		fallback func(target string) string
		wantExec [][]string
		retry    bool
	}{
		{
			name:   "origin file string equals receiver target",
			origin: func(target string) string { return "file:" + target },
			retry:  true,
		},
		{
			name:     "fallback file string equals receiver target",
			origin:   func(string) string { return "codex:thread-0001" },
			fallback: func(target string) string { return "file:" + target },
			wantExec: [][]string{codexArgv("thread-0001", text)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			target := filepath.Join(t.TempDir(), "example-notice.txt")
			var origin, fallback string
			if tc.origin != nil {
				origin = tc.origin(target)
			}
			if tc.fallback != nil {
				fallback = tc.fallback(target)
			}
			msg := Message{
				QuestionID: "q-file-dedup",
				Objective:  "obj",
				Origin:     origin,
				Fallback:   fallback,
				Text:       text,
				Home:       home,
				Receivers: []Receiver{{
					ID:      "example-manager",
					Adapter: "file",
					Target:  target,
					Enabled: true,
					Scope:   Scope{All: true},
				}},
			}
			if target == inboxPath(home, msg.QuestionID) {
				t.Fatal("target collided with the home inbox")
			}
			var commands [][]string
			cur := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
			svc := New(func(_ context.Context, argv []string) error {
				commands = append(commands, append([]string{}, argv...))
				return nil
			}, func() time.Time { return cur })
			check := func() {
				t.Helper()
				if err := svc.Deliver(context.Background(), msg); err != nil {
					t.Fatal(err)
				}
				body, err := os.ReadFile(target)
				if err != nil || strings.Count(string(body), text) != 1 {
					t.Fatalf("target %v %s", err, body)
				}
				inboxBody, err := os.ReadFile(inboxPath(home, msg.QuestionID))
				if err != nil || strings.Count(string(inboxBody), text) != 1 {
					t.Fatalf("inbox %v %s", err, inboxBody)
				}
				if !reflect.DeepEqual(commands, tc.wantExec) {
					t.Fatalf("commands %#v", commands)
				}
			}
			check()
			if !tc.retry {
				return
			}
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(inboxPath(home, msg.QuestionID)); err != nil {
				t.Fatal(err)
			}
			st := svc.sent[msg.QuestionID]
			cur = st.at.Add(st.gap + time.Nanosecond)
			check()
		})
	}
}
