package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNotifyHelpNamesPurpose(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Main([]string{"notify", "--help"}, &out, &errb); code != 0 {
		t.Fatalf("code %d %s", code, errb.String())
	}
	text := out.String()
	for _, part := range []string{"purpose:", "parameters:", "add", "list", "remove", "test", "--from-origin"} {
		if !strings.Contains(text, part) {
			t.Fatalf("help missing %s\n%s", part, text)
		}
	}
}

func TestNotifyAddListRemoveFromOrigin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CARDEX_ORIGIN", "codex:thread-0001")
	code, out, errb := runNotify(t, home, "add", "--id", "example-manager", "--from-origin")
	if code != 0 {
		t.Fatalf("add %d %s", code, errb)
	}
	if !strings.Contains(out, "example-manager") || !strings.Contains(out, "codex") || !strings.Contains(out, "enabled") {
		t.Fatalf("add out %s", out)
	}
	code, listed, errb := runNotify(t, home, "list")
	if code != 0 || !strings.Contains(listed, "example-manager\tcodex\tthread-0001\tenabled") {
		t.Fatalf("list %d %s %s", code, listed, errb)
	}
	before := mustRead(t, filepath.Join(home, "config.json"))
	code, _, errb = runNotify(t, home, "add", "--id", "example-manager", "--from-origin")
	if code == 0 || !strings.Contains(errb, "already exists") {
		t.Fatalf("duplicate %d %s", code, errb)
	}
	if after := mustRead(t, filepath.Join(home, "config.json")); after != before {
		t.Fatal("duplicate changed the file")
	}
	code, again, _ := runNotify(t, home, "list")
	if code != 0 || again != listed {
		t.Fatalf("list changed %q %q", again, listed)
	}
	code, removed, errb := runNotify(t, home, "remove", "example-manager")
	if code != 0 || !strings.Contains(removed, "removed example-manager") {
		t.Fatalf("remove %d %s %s", code, removed, errb)
	}
	code, left, _ := runNotify(t, home, "list")
	if code != 0 || strings.Contains(left, "example-manager") {
		t.Fatalf("still listed %s", left)
	}
}

func TestNotifyTestFakeCodexAndHermesAck(t *testing.T) {
	t.Run("codex", func(t *testing.T) {
		home := t.TempDir()
		logPath := filepath.Join(t.TempDir(), "codex.log")
		plantNotifyCodex(t, logPath)
		code, _, errb := runNotify(t, home, "add", "--id", "example-manager", "--adapter", "codex", "--target", "thread-0001")
		if code != 0 {
			t.Fatal(errb)
		}
		code, out, errb := runNotify(t, home, "test", "example-manager")
		if code != 0 || !strings.Contains(out, "received example-manager") {
			t.Fatalf("test %d %s %s", code, out, errb)
		}
		logged := mustRead(t, logPath)
		if !strings.Contains(logged, "queue") || !strings.Contains(logged, "thread-0001") {
			t.Fatalf("codex log %s", logged)
		}
	})
	t.Run("hermes-ack", func(t *testing.T) {
		home := t.TempDir()
		root := t.TempDir()
		code, _, errb := runNotify(t, home, "add", "--id", "example-manager", "--from-origin", "--origin", "hermes:profile-a/example-manager", "--root", root)
		if code != 0 {
			t.Fatal(errb)
		}
		code, listed, errb := runNotify(t, home, "list")
		if code != 0 || !strings.Contains(listed, "example-manager\thermes\tprofile-a/example-manager\tenabled") {
			t.Fatalf("list %d %s %s", code, listed, errb)
		}
		done := make(chan struct{})
		go writeHermesAckWhenWakeAppears(t, root, "example-manager", done)
		code, out, errb := runNotify(t, home, "test", "example-manager", "--wait", "3s")
		close(done)
		if code != 0 || !strings.Contains(out, "waiting ") || !strings.Contains(out, "received example-manager") {
			t.Fatalf("test %d %s %s", code, out, errb)
		}
		if !strings.Contains(out, ".ack.json") {
			t.Fatalf("ack path not printed %s", out)
		}
	})
	t.Run("hermes-timeout", func(t *testing.T) {
		home := t.TempDir()
		root := t.TempDir()
		code, _, errb := runNotify(t, home, "add", "--id", "example-manager", "--adapter", "hermes", "--target", "profile-a/example-manager", "--root", root)
		if code != 0 {
			t.Fatal(errb)
		}
		code, out, errb := runNotify(t, home, "test", "example-manager", "--wait", "50ms")
		if code == 0 || strings.Contains(out, "received") || !strings.Contains(errb, "not received") {
			t.Fatalf("timeout %d %s %s", code, out, errb)
		}
	})
}

func runNotify(t *testing.T, home string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	argv := append([]string{"--home", home, "notify"}, args...)
	code := Main(argv, &out, &errb)
	return code, out.String(), errb.String()
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func plantNotifyCodex(t *testing.T, logPath string) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$CARDEX_CODEX_LOG\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CARDEX_CODEX_LOG", logPath)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func writeHermesAckWhenWakeAppears(t *testing.T, root, id string, done <-chan struct{}) {
	t.Helper()
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
			ack := map[string]any{
				"schema":         "cardex.manager_wake.hermes_ack.v1",
				"wake_event_ids": inbound.WakeEventIDs,
				"inbound_digest": inbound.InboundDigest,
				"received_at":    time.Now().UTC().Format(time.RFC3339Nano),
			}
			body, err := json.Marshal(ack)
			if err != nil {
				return
			}
			wakeID := strings.TrimSuffix(name, ".json")
			_ = os.WriteFile(filepath.Join(dir, wakeID+".ack.json"), append(body, '\n'), 0o644)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
