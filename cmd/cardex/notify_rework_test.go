package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNotifyTestHermesAckBelongsToThisInvocation(t *testing.T) {
	const id = "example-manager"
	cases := []struct {
		name      string
		readd     bool
		ackSecond bool
	}{
		{name: "stale ack is not this test", ackSecond: false},
		{name: "remove and add keeps the old ack from counting", readd: true, ackSecond: false},
		{name: "ack of the wake this test created", ackSecond: true},
		{name: "readd then ack the new wake", readd: true, ackSecond: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			root := t.TempDir()
			addHermes := func() {
				t.Helper()
				code, _, errb := runNotify(t, home, "add", "--id", id, "--adapter", "hermes", "--target", "profile-a/example-manager", "--root", root)
				if code != 0 {
					t.Fatal(errb)
				}
			}
			addHermes()
			first := runHermesNotifyTest(t, home, root, id, true)
			if first.code != 0 || !strings.Contains(first.out, "received "+id) {
				t.Fatalf("first test %d %s %s", first.code, first.out, first.errb)
			}
			if _, err := os.Stat(first.ackPath); err != nil {
				t.Fatalf("first ack %v", err)
			}
			if tc.readd {
				code, _, errb := runNotify(t, home, "remove", id)
				if code != 0 {
					t.Fatal(errb)
				}
				addHermes()
				if _, err := os.Stat(first.ackPath); err != nil {
					t.Fatalf("old ack removed with the receiver %v", err)
				}
			}
			second := runHermesNotifyTest(t, home, root, id, tc.ackSecond)
			if second.ackPath == first.ackPath {
				t.Fatalf("second test reused wake %s", second.ackPath)
			}
			if tc.ackSecond {
				if second.code != 0 || !strings.Contains(second.out, "received "+id) {
					t.Fatalf("new ack %d %s %s", second.code, second.out, second.errb)
				}
				if _, err := os.Stat(second.ackPath); err != nil {
					t.Fatalf("new ack missing %v", err)
				}
				return
			}
			if second.code == 0 || strings.Contains(second.out, "received "+id) {
				t.Fatalf("stale ack reported received\n%s", second.out)
			}
			if !strings.Contains(second.errb, "not received "+id) {
				t.Fatalf("stderr %s", second.errb)
			}
			if _, err := os.Stat(second.ackPath); err == nil {
				t.Fatal("unacked test gained an ack")
			}
		})
	}
}

type hermesTestResult struct {
	code    int
	out     string
	errb    string
	ackPath string
}

func runHermesNotifyTest(t *testing.T, home, root, id string, ack bool) hermesTestResult {
	t.Helper()
	done := make(chan struct{})
	if ack {
		go ackUnackedHermesWake(root, id, done)
	}
	wait := "200ms"
	if ack {
		wait = "3s"
	}
	code, out, errb := runNotify(t, home, "test", id, "--wait", wait)
	close(done)
	return hermesTestResult{code: code, out: out, errb: errb, ackPath: waitingAckPath(out)}
}

func waitingAckPath(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if path, ok := strings.CutPrefix(line, "waiting "); ok {
			return strings.TrimSpace(path)
		}
	}
	return ""
}

func ackUnackedHermesWake(root, id string, done <-chan struct{}) {
	dir := filepath.Join(root, "control", "manager-wake", "hermes-inbox", id)
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			return
		default:
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".ack.json") {
				continue
			}
			wakeID := strings.TrimSuffix(name, ".json")
			ackPath := filepath.Join(dir, wakeID+".ack.json")
			if _, err := os.Stat(ackPath); err == nil {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			var inbound struct {
				WakeEventIDs  []string `json:"wake_event_ids"`
				InboundDigest string   `json:"inbound_digest"`
			}
			if json.Unmarshal(raw, &inbound) != nil || inbound.InboundDigest == "" || len(inbound.WakeEventIDs) == 0 {
				continue
			}
			body, err := json.Marshal(map[string]any{
				"schema":         "cardex.manager_wake.hermes_ack.v1",
				"wake_event_ids": inbound.WakeEventIDs,
				"inbound_digest": inbound.InboundDigest,
				"received_at":    time.Now().UTC().Format(time.RFC3339Nano),
			})
			if err != nil {
				return
			}
			if err := os.WriteFile(ackPath, append(body, '\n'), 0o644); err != nil {
				return
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNotifyTestFileWritesTarget(t *testing.T) {
	const id = "example-file"
	text := "cardex notify test " + id
	cases := []struct {
		name      string
		writable  bool
		wantCode  int
		wantRecvd bool
	}{
		{name: "target write reports received", writable: true, wantCode: 0, wantRecvd: true},
		{name: "inbox alone is not received", writable: false, wantCode: 1, wantRecvd: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			var target string
			if tc.writable {
				target = filepath.Join(t.TempDir(), "notes", "example-notice.txt")
			} else {
				blocker := filepath.Join(t.TempDir(), "not-a-directory")
				if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
				target = filepath.Join(blocker, "example-notice.txt")
				inbox := filepath.Join(home, "inbox")
				if err := os.MkdirAll(inbox, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(inbox, "already.txt"), []byte(text+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			code, _, errb := runNotify(t, home, "add", "--id", id, "--adapter", "file", "--target", target, "--all")
			if code != 0 {
				t.Fatal(errb)
			}
			code, out, errb := runNotify(t, home, "test", id)
			got := strings.Contains(out, "received "+id)
			if code != tc.wantCode || got != tc.wantRecvd {
				t.Fatalf("test %d received %v\n%s\n%s", code, got, out, errb)
			}
			body, err := os.ReadFile(target)
			if tc.writable {
				if err != nil || !strings.Contains(string(body), text) || strings.Count(string(body), text) != 1 {
					t.Fatalf("target %v %s", err, body)
				}
				if strings.Contains(target, filepath.Join(home, "inbox")) {
					t.Fatalf("target is the home inbox %s", target)
				}
				return
			}
			if err == nil && strings.Contains(string(body), text) {
				t.Fatalf("unwritable target received %s", body)
			}
			inboxBody, err := os.ReadFile(filepath.Join(home, "inbox", "already.txt"))
			if err != nil || !strings.Contains(string(inboxBody), text) {
				t.Fatalf("inbox seed %v %s", err, inboxBody)
			}
		})
	}
}
