package notify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
