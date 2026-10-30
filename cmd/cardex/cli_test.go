package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cardex/internal/model"
)

func TestHelpListsCommands(t *testing.T) {
	var out bytes.Buffer
	if code := Main([]string{"--help"}, &out, io.Discard); code != 0 {
		t.Fatal(code)
	}
	for _, name := range []string{
		"serve", "project", "objective", "node", "card", "question", "channel",
		"route", "positioning", "schedule", "scorecard", "import-legacy", "doctor", "host",
		"add", "list", "show", "set", "pause", "resume", "cancel", "freeze", "reopen",
		"retry", "revive", "answer", "check", "explain", "proposals", "approve", "reject", "run",
	} {
		if !strings.Contains(out.String(), name) {
			t.Errorf("help missing %s\n%s", name, out.String())
		}
	}
}

func TestUnimplementedLanes(t *testing.T) {
	cases := []struct {
		args []string
		lane string
	}{
		{[]string{"project", "set"}, "R4-B"},
		{[]string{"objective", "pause"}, "R4-B"},
		{[]string{"card", "cancel"}, "R4-B"},
		{[]string{"import-legacy", "--dry-run", "v1"}, "R4-B"},
		{[]string{"doctor"}, "R4-B"},
		{[]string{"node", "list"}, "R3-A"},
		{[]string{"question", "show", "q"}, "R3-A"},
		{[]string{"channel", "check"}, "R2-D"},
		{[]string{"route", "explain", "c1"}, "R2-D"},
		{[]string{"positioning", "approve", "p"}, "R4-C"},
		{[]string{"scorecard"}, "R4-C"},
		{[]string{"schedule", "run", "weekly"}, "R3-C"},
		{[]string{"host", "attempt-1"}, "R2-A"},
	}
	for _, tc := range cases {
		var out bytes.Buffer
		if code := Main(tc.args, &out, io.Discard); code != 0 {
			t.Fatalf("%v code %d", tc.args, code)
		}
		want := "未实现（由 " + tc.lane + " 负责）\n"
		if out.String() != want {
			t.Fatalf("%v got %q", tc.args, out.String())
		}
	}
}

func TestProjectObjectiveCardRoundTrip(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	var out, errb bytes.Buffer
	code := Main([]string{"--home", home, "--json", "project", "add", "--id", "p1", "--repo", repo, "--test-cmd", "go test ./...", "--design-dir", "docs/design/"}, &out, &errb)
	if code != 0 {
		t.Fatalf("add project %d %s", code, errb.String())
	}
	var project model.Project
	if err := json.Unmarshal(out.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	if project.ID != "p1" || project.RepoPath != repo || project.TestCmd != "go test ./..." || project.DesignDir != "docs/design/" {
		t.Fatalf("project %+v", project)
	}
	out.Reset()
	if code := Main([]string{"--home", home, "--json", "project", "list"}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	var projects []model.Project
	if err := json.Unmarshal(out.Bytes(), &projects); err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].RepoPath != repo {
		t.Fatalf("list %+v", projects)
	}
	out.Reset()
	if code := Main([]string{"--home", home, "project", "show", "--json", "p1"}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	var shown model.Project
	if err := json.Unmarshal(out.Bytes(), &shown); err != nil {
		t.Fatal(err)
	}
	if shown.ID != project.ID || shown.RepoPath != project.RepoPath || shown.WorktreeRoot != project.WorktreeRoot {
		t.Fatalf("show %+v add %+v", shown, project)
	}

	out.Reset()
	errb.Reset()
	code = Main([]string{"--home", home, "--json", "objective", "add", "--id", "o1", "--project", "p1", "--title", "Ship it", "--acceptance", "go test ./...", "--mode", "manual", "--freeze-mode", "fast"}, &out, &errb)
	if code != 0 {
		t.Fatalf("add objective %d %s", code, errb.String())
	}
	var objective model.Objective
	if err := json.Unmarshal(out.Bytes(), &objective); err != nil {
		t.Fatal(err)
	}
	if objective.Title != "Ship it" || objective.Project != "p1" || objective.Mode != model.ModeManual || objective.FreezeMode != model.FreezeFast {
		t.Fatalf("objective %+v", objective)
	}
	out.Reset()
	if code := Main([]string{"--home", home, "--json", "objective", "list"}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	var objectives []model.Objective
	if err := json.Unmarshal(out.Bytes(), &objectives); err != nil {
		t.Fatal(err)
	}
	if len(objectives) != 1 || objectives[0].Title != "Ship it" {
		t.Fatalf("objectives %+v", objectives)
	}
	out.Reset()
	if code := Main([]string{"--home", home, "--json", "objective", "show", "o1"}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	var oshown model.Objective
	if err := json.Unmarshal(out.Bytes(), &oshown); err != nil {
		t.Fatal(err)
	}
	if oshown.Title != "Ship it" || oshown.IntegrationBranch != "cardex/o1/main" {
		t.Fatalf("show objective %+v", oshown)
	}

	out.Reset()
	errb.Reset()
	code = Main([]string{"--home", home, "--json", "card", "add", "--id", "c1", "--objective", "o1", "--title", "Write parser", "--kind", "dev", "--body", "parse it", "--claim", "calc.go=Answer"}, &out, &errb)
	if code != 0 {
		t.Fatalf("add card %d %s", code, errb.String())
	}
	var card model.Card
	if err := json.Unmarshal(out.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	if card.Title != "Write parser" || card.Kind != model.KindDev || card.Body != "parse it" || len(card.Claims) != 1 || card.Claims[0].Path != "calc.go" || card.Claims[0].Symbol != "Answer" {
		t.Fatalf("card %+v", card)
	}
	out.Reset()
	if code := Main([]string{"--home", home, "--json", "card", "list", "--objective", "o1"}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	var cards []model.Card
	if err := json.Unmarshal(out.Bytes(), &cards); err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[0].Title != "Write parser" || cards[0].Claims[0].Symbol != "Answer" {
		t.Fatalf("cards %+v", cards)
	}
	out.Reset()
	if code := Main([]string{"--home", home, "--json", "card", "show", "c1"}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	var cshown model.Card
	if err := json.Unmarshal(out.Bytes(), &cshown); err != nil {
		t.Fatal(err)
	}
	if cshown.Body != "parse it" || cshown.Objective != "o1" {
		t.Fatalf("show card %+v", cshown)
	}
}

func TestCardAddSoftInputs(t *testing.T) {
	cases := []struct {
		name  string
		extra []string
		want  func(t *testing.T, card model.Card, raw string)
	}{
		{
			name:  "missing title",
			extra: nil,
			want: func(t *testing.T, card model.Card, _ string) {
				if card.Status != model.CardNeedsInput {
					t.Fatalf("status %s", card.Status)
				}
				if card.Title == "" {
					t.Fatal("missing placeholder title")
				}
			},
		},
		{
			name:  "tier t1",
			extra: []string{"--title", "Tier one", "--tier", "t1"},
			want: func(t *testing.T, card model.Card, _ string) {
				if card.Tier != model.TierT1 || card.Status != model.CardReady {
					t.Fatalf("tier %s status %s", card.Tier, card.Status)
				}
			},
		},
		{
			name:  "tier t2",
			extra: []string{"--title", "Tier card", "--tier", "t2"},
			want: func(t *testing.T, card model.Card, _ string) {
				if card.Tier != model.TierT2 || card.Status != model.CardReady {
					t.Fatalf("tier %s status %s", card.Tier, card.Status)
				}
			},
		},
		{
			name:  "tier t3",
			extra: []string{"--title", "Tier three", "--tier", "t3"},
			want: func(t *testing.T, card model.Card, _ string) {
				if card.Tier != model.TierT3 || card.Status != model.CardReady {
					t.Fatalf("tier %s status %s", card.Tier, card.Status)
				}
			},
		},
		{
			name:  "empty kind",
			extra: []string{"--title", "Kind card", "--kind", ""},
			want: func(t *testing.T, card model.Card, _ string) {
				if card.Kind != model.KindDev || card.Status != model.CardReady {
					t.Fatalf("kind %s status %s", card.Kind, card.Status)
				}
			},
		},
		{
			name:  "kind DEV",
			extra: []string{"--title", "Dev card", "--kind", "DEV"},
			want: func(t *testing.T, card model.Card, _ string) {
				if card.Kind != model.KindDev || card.Status != model.CardReady {
					t.Fatalf("kind %s status %s", card.Kind, card.Status)
				}
				if card.Title != "Dev card" {
					t.Fatalf("title %q", card.Title)
				}
			},
		},
		{
			name:  "empty status",
			extra: []string{"--title", "Empty status", "--status", ""},
			want: func(t *testing.T, card model.Card, _ string) {
				if card.Status != model.CardReady {
					t.Fatalf("status %s", card.Status)
				}
				if card.Title != "Empty status" {
					t.Fatalf("title %q", card.Title)
				}
			},
		},
		{
			name:  "status READY",
			extra: []string{"--title", "Ready card", "--status", "READY"},
			want: func(t *testing.T, card model.Card, _ string) {
				if card.Status != model.CardReady {
					t.Fatalf("status %s", card.Status)
				}
			},
		},
		{
			name:  "empty claim",
			extra: []string{"--title", "Claim card", "--claim", ""},
			want: func(t *testing.T, card model.Card, _ string) {
				if len(card.Claims) != 0 {
					t.Fatalf("claims %+v", card.Claims)
				}
				if card.Status != model.CardReady {
					t.Fatalf("status %s", card.Status)
				}
			},
		},
		{
			name:  "tier T4",
			extra: []string{"--title", "Bad tier", "--tier", "T4"},
			want: func(t *testing.T, card model.Card, raw string) {
				assertNeedsInput(t, card, "Bad tier")
				if card.Tier != model.TierNone {
					t.Fatalf("tier %q", card.Tier)
				}
				if !strings.Contains(raw, "T4") || !strings.Contains(card.WaitReason, "T4") {
					t.Fatalf("json %s wait %q", raw, card.WaitReason)
				}
			},
		},
		{
			name:  "unknown kind",
			extra: []string{"--title", "Odd kind", "--kind", "WIDGET"},
			want: func(t *testing.T, card model.Card, raw string) {
				assertNeedsInput(t, card, "Odd kind")
				if card.Kind != model.KindDev {
					t.Fatalf("kind %s", card.Kind)
				}
				if !strings.Contains(raw, "WIDGET") || !strings.Contains(card.WaitReason, "WIDGET") {
					t.Fatalf("json %s wait %q", raw, card.WaitReason)
				}
			},
		},
		{
			name:  "unknown status",
			extra: []string{"--title", "Odd status", "--status", "BOGUS"},
			want: func(t *testing.T, card model.Card, raw string) {
				assertNeedsInput(t, card, "Odd status")
				if !strings.Contains(raw, "BOGUS") || !strings.Contains(card.WaitReason, "BOGUS") {
					t.Fatalf("json %s wait %q", raw, card.WaitReason)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			repo := t.TempDir()
			var out, errb bytes.Buffer
			code := Main([]string{"--home", home, "project", "add", "--id", "p1", "--repo", repo, "--test-cmd", "go test ./...", "--design-dir", "docs/design/"}, &out, &errb)
			if code != 0 {
				t.Fatalf("project %d %s", code, errb.String())
			}
			out.Reset()
			errb.Reset()
			code = Main([]string{"--home", home, "objective", "add", "--id", "o1", "--project", "p1", "--title", "Ship it", "--acceptance", "go test ./..."}, &out, &errb)
			if code != 0 {
				t.Fatalf("objective %d %s", code, errb.String())
			}
			out.Reset()
			errb.Reset()
			args := []string{"--home", home, "card", "add", "--objective", "o1"}
			args = append(args, tc.extra...)
			code = Main(args, &out, &errb)
			if code != 0 {
				t.Fatalf("add %d %s", code, errb.String())
			}
			if strings.Contains(errb.String(), "claim path required") {
				t.Fatalf("stderr %s", errb.String())
			}
			out.Reset()
			errb.Reset()
			code = Main([]string{"--home", home, "card", "list", "--json"}, &out, &errb)
			if code != 0 {
				t.Fatalf("list %d %s", code, errb.String())
			}
			var cards []model.Card
			if err := json.Unmarshal(out.Bytes(), &cards); err != nil {
				t.Fatal(err)
			}
			if len(cards) != 1 || cards[0].Objective != "o1" {
				t.Fatalf("list %s", out.String())
			}
			tc.want(t, cards[0], out.String())
			t.Logf("exit 0 status=%s tier=%s kind=%s claims=%d title=%q wait=%q", cards[0].Status, cards[0].Tier, cards[0].Kind, len(cards[0].Claims), cards[0].Title, cards[0].WaitReason)
		})
	}
}

func assertNeedsInput(t *testing.T, card model.Card, title string) {
	t.Helper()
	if !card.Kind.Valid() || !card.Tier.Valid() || !card.Status.Valid() {
		t.Fatalf("illegal kind=%s tier=%q status=%s", card.Kind, card.Tier, card.Status)
	}
	if card.Status != model.CardNeedsInput {
		t.Fatalf("status %s", card.Status)
	}
	if card.Title != title {
		t.Fatalf("title %q", card.Title)
	}
}

func TestServeLoopsUntilStopped(t *testing.T) {
	if code := runServe(t.Context(), t.TempDir(), "127.0.0.1:8787", io.Discard, io.Discard); code == 0 {
		t.Fatal("accepted port 8787")
	}
	if code := runServe(t.Context(), t.TempDir(), "127.0.0.1:8788", io.Discard, io.Discard); code == 0 {
		t.Fatal("accepted port 8788")
	}
	ctx, cancel := context.WithCancel(context.Background())
	var buf lockBuf
	done := make(chan int, 1)
	go func() {
		done <- runServe(ctx, t.TempDir(), "127.0.0.1:0", &buf, io.Discard)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(buf.String(), "serve stub-loop iteration") < 2 {
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("serve output:\n%s", buf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if strings.Contains(buf.String(), "fatal") {
		t.Fatalf("fatal in output:\n%s", buf.String())
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("serve exit %d\n%s", code, buf.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not return")
	}
}

func TestRefusesUserCardexHome(t *testing.T) {
	user, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	var errb bytes.Buffer
	code := Main([]string{"--home", filepath.Join(user, ".cardex"), "project", "list"}, io.Discard, &errb)
	if code == 0 || !strings.Contains(errb.String(), ".cardex") {
		t.Fatalf("code %d err %s", code, errb.String())
	}
}

type lockBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
