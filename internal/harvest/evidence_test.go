package harvest

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

const (
	testEdge  = 64 << 10
	testLimit = 1 << 20
)

func TestTruncateEvidence(t *testing.T) {
	small := []byte("acceptance output")
	got := truncateEvidence(small)
	if !bytes.Equal(got, small) {
		t.Fatalf("small %q", got)
	}
	small[0] = 'X'
	if bytes.Contains(got, []byte("X")) {
		t.Fatal("truncateEvidence kept the caller's buffer")
	}

	exact := bytes.Repeat([]byte("E"), testLimit)
	if !bytes.Equal(truncateEvidence(exact), exact) {
		t.Fatal("a 1 MiB body was truncated")
	}

	over := bytes.Repeat([]byte("B"), testLimit+25)
	over[0] = 'H'
	over[len(over)-1] = 'T'
	assertTruncated(t, over, truncateEvidence(over))

	nl := bytes.Repeat([]byte("C"), testLimit+40)
	nl[testEdge-1] = '\n'
	nl[0] = 'H'
	nl[len(nl)-1] = 'T'
	assertTruncated(t, nl, truncateEvidence(nl))
}

func TestHarvestEvidenceFiles(t *testing.T) {
	t.Run("kinds copy a result and still record an empty acceptance log", func(t *testing.T) {
		cases := []struct {
			kind model.CardKind
			body string
		}{
			{model.KindReview, "## 复核结论\npass\n"},
			{model.KindDesign, "## 结论\nship the design\n"},
			{model.KindResearch, "## 事实\nfound the bound\n"},
			{model.KindDev, "## 结论\ndev note\n"},
		}
		for _, tc := range cases {
			t.Run(string(tc.kind), func(t *testing.T) {
				dir, head := newRepo(t)
				home := t.TempDir()
				write(t, dir, ".cardex-result.md", tc.body)
				in := harvestInput(dir, head, model.Card{ID: "c", Kind: tc.kind}, 0, nil, "")
				in.Home = home
				in.Attempt.ID = "att-" + string(tc.kind)
				res, err := Harvest(context.Background(), in)
				if err != nil || res.EvidenceErr != nil {
					t.Fatalf("harvest %v evidence %v", err, res.EvidenceErr)
				}
				acc := filepath.Join(home, "attempts", in.Attempt.ID, "acceptance.log")
				info, err := os.Stat(acc)
				if err != nil || info.Size() != 0 {
					t.Fatalf("acceptance.log %v size %d", err, sizeOf(info))
				}
				got, err := os.ReadFile(filepath.Join(home, "attempts", in.Attempt.ID, "result.md"))
				if err != nil || string(got) != tc.body {
					t.Fatalf("result.md %q %v", got, err)
				}
			})
		}
	})

	t.Run("missing result writes no result.md", func(t *testing.T) {
		dir, head := newRepo(t)
		home := t.TempDir()
		in := harvestInput(dir, head, model.Card{ID: "c", Kind: model.KindReview}, 0, nil, "")
		in.Home = home
		res, err := Harvest(context.Background(), in)
		if err != nil || res.EvidenceErr != nil {
			t.Fatalf("harvest %v evidence %v", err, res.EvidenceErr)
		}
		if _, err := os.Stat(filepath.Join(home, "attempts", "att", "result.md")); !os.IsNotExist(err) {
			t.Fatalf("result.md err %v", err)
		}
		if _, err := os.Stat(filepath.Join(home, "attempts", "att", "acceptance.log")); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("empty result file is still copied", func(t *testing.T) {
		dir, head := newRepo(t)
		home := t.TempDir()
		write(t, dir, ".cardex-result.md", "")
		in := harvestInput(dir, head, model.Card{ID: "c", Kind: model.KindResearch}, 0, nil, "")
		in.Home = home
		res, err := Harvest(context.Background(), in)
		if err != nil || res.EvidenceErr != nil {
			t.Fatalf("harvest %v evidence %v", err, res.EvidenceErr)
		}
		got, err := os.ReadFile(filepath.Join(home, "attempts", "att", "result.md"))
		if err != nil || len(got) != 0 {
			t.Fatalf("result.md %q %v", got, err)
		}
	})

	t.Run("blank home writes nothing", func(t *testing.T) {
		dir, head := newRepo(t)
		cwd := t.TempDir()
		t.Chdir(cwd)
		write(t, dir, ".cardex-result.md", "## 事实\nx\n")
		in := harvestInput(dir, head, model.Card{ID: "c", Kind: model.KindResearch}, 0, nil, "")
		in.Home = " \t "
		res, err := Harvest(context.Background(), in)
		if err != nil || res.EvidenceErr != nil {
			t.Fatalf("harvest %v evidence %v", err, res.EvidenceErr)
		}
		for _, root := range []string{cwd, dir} {
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Name() == "attempts" {
					t.Fatalf("wrote attempts under %s", root)
				}
			}
		}
		if _, err := os.Stat(filepath.Join("attempts", "att", "acceptance.log")); !os.IsNotExist(err) {
			t.Fatalf("relative acceptance.log: %v", err)
		}
		if _, err := os.Stat(filepath.Join("attempts", "att", "result.md")); !os.IsNotExist(err) {
			t.Fatalf("relative result.md: %v", err)
		}
	})

	t.Run("trailing space home is joined as given", func(t *testing.T) {
		dir, head := newRepo(t)
		parent := t.TempDir()
		home := filepath.Join(parent, "home ")
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		trimmed := strings.TrimSpace(home)
		if trimmed == home {
			t.Fatal("fixture lost its trailing space")
		}
		body := "## 事实\nkept\n"
		write(t, dir, ".cardex-result.md", body)
		in := harvestInput(dir, head, model.Card{ID: "c", Kind: model.KindResearch}, 0, nil, "")
		in.Home = home
		res, err := Harvest(context.Background(), in)
		if err != nil || res.EvidenceErr != nil {
			t.Fatalf("harvest %v evidence %v", err, res.EvidenceErr)
		}
		acc := filepath.Join(home, "attempts", in.Attempt.ID, "acceptance.log")
		if _, err := os.Stat(acc); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(home, "attempts", in.Attempt.ID, "result.md"))
		if err != nil || string(got) != body {
			t.Fatalf("result.md %q %v", got, err)
		}
		if _, err := os.Stat(filepath.Join(trimmed, "attempts", in.Attempt.ID, "acceptance.log")); !os.IsNotExist(err) {
			t.Fatalf("acceptance.log under trimmed home: %v", err)
		}
		if _, err := os.Stat(filepath.Join(trimmed, "attempts", in.Attempt.ID, "result.md")); !os.IsNotExist(err) {
			t.Fatalf("result.md under trimmed home: %v", err)
		}
	})

	t.Run("unsafe attempt id does not write", func(t *testing.T) {
		dir, head := newRepo(t)
		home := t.TempDir()
		write(t, dir, ".cardex-result.md", "## 事实\nx\n")
		in := harvestInput(dir, head, model.Card{ID: "c", Kind: model.KindResearch}, 0, nil, "")
		in.Home = home
		in.Attempt.ID = "a/b"
		res, err := Harvest(context.Background(), in)
		if err != nil || res.EvidenceErr == nil {
			t.Fatalf("err %v evidence %v", err, res.EvidenceErr)
		}
		if _, err := os.Stat(filepath.Join(home, "attempts")); !os.IsNotExist(err) {
			t.Fatalf("wrote under home: %v", err)
		}
	})

	t.Run("each command is prefixed and the excerpt is raw output", func(t *testing.T) {
		dir, head := newRepo(t)
		home := t.TempDir()
		in := harvestInput(dir, head, model.Card{
			ID: "c", Kind: model.KindDev, Acceptance: []string{"printf hello", "false"},
		}, 0, []byte("OUTPUT-SECRET"), "")
		in.Home = home
		res, err := Harvest(context.Background(), in)
		if err != nil || res.EvidenceErr != nil {
			t.Fatalf("harvest %v evidence %v", err, res.EvidenceErr)
		}
		if res.Outcome != model.OutcomeTestsFailed {
			t.Fatalf("outcome %s", res.Outcome)
		}
		log, err := os.ReadFile(filepath.Join(home, "attempts", "att", "acceptance.log"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(log)
		if !strings.HasPrefix(text, "command=\"printf hello\" exit=0 duration=") || !strings.Contains(text, "hello\ncommand=\"false\" exit=1 duration=") {
			t.Fatalf("log %q", text)
		}
		falseAt := strings.Index(text, "command=\"false\" exit=1 duration=")
		if falseAt <= 0 || text[falseAt-1] != '\n' {
			t.Fatalf("second header is not on its own line: %q", text)
		}
		if strings.Contains(text, "hellocommand=") {
			t.Fatalf("header glued to raw output: %q", text)
		}
		if res.Excerpt != "hello" || strings.Contains(res.Excerpt, "command=") || strings.Contains(res.Excerpt, "OUTPUT-SECRET") {
			t.Fatalf("excerpt %q", res.Excerpt)
		}
	})

	t.Run("a header stays one line when output already ends with a newline", func(t *testing.T) {
		dir, head := newRepo(t)
		home := t.TempDir()
		in := harvestInput(dir, head, model.Card{
			ID: "c", Kind: model.KindDev, Acceptance: []string{"printf 'hello\n'", "false"},
		}, 0, nil, "")
		in.Home = home
		res, err := Harvest(context.Background(), in)
		if err != nil || res.EvidenceErr != nil {
			t.Fatalf("harvest %v evidence %v", err, res.EvidenceErr)
		}
		log, err := os.ReadFile(filepath.Join(home, "attempts", "att", "acceptance.log"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(log)
		if !strings.Contains(text, "hello\ncommand=\"false\" exit=1 duration=") || strings.Contains(text, "hello\n\ncommand=") {
			t.Fatalf("log %q", text)
		}
		if res.Excerpt != "hello" {
			t.Fatalf("excerpt %q", res.Excerpt)
		}
	})

	t.Run("prefix past 2000 characters stays in the log", func(t *testing.T) {
		dir, head := newRepo(t)
		home := t.TempDir()
		prefix := "PREFIX-LONG-9f3a"
		payload := prefix + strings.Repeat("q", 2500) + "TAIL-LONG-9f3a"
		write(t, dir, "payload.bin", payload)
		in := harvestInput(dir, head, model.Card{
			ID: "c", Kind: model.KindDev, Acceptance: []string{"cat payload.bin; exit 1"},
		}, 1, []byte("OUTPUT-SECRET"), "")
		in.Home = home
		res, err := Harvest(context.Background(), in)
		if err != nil || res.EvidenceErr != nil {
			t.Fatalf("harvest %v evidence %v", err, res.EvidenceErr)
		}
		log, err := os.ReadFile(filepath.Join(home, "attempts", "att", "acceptance.log"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(log), prefix) || !strings.Contains(string(log), "command=\"cat payload.bin; exit 1\" exit=1 duration=") {
			t.Fatalf("log missing prefix or header")
		}
		if strings.Contains(res.Excerpt, prefix) || !strings.Contains(res.Excerpt, "TAIL-LONG-9f3a") || len(res.Excerpt) != 2000 {
			t.Fatalf("excerpt %q len %d", res.Excerpt, len(res.Excerpt))
		}
		if _, err := os.Stat(filepath.Join(home, "attempts", "att", "result.md")); !os.IsNotExist(err) {
			t.Fatal("result.md written without a result file")
		}
	})

	t.Run("acceptance log over 1 MiB names the omitted bytes", func(t *testing.T) {
		dir, head := newRepo(t)
		home := t.TempDir()
		payload := bytes.Repeat([]byte("A"), testLimit+100)
		copy(payload[:8], []byte("HEADMARK"))
		copy(payload[80<<10:], []byte("MIDMARK-GONE"))
		copy(payload[len(payload)-8:], []byte("TAILMARK"))
		write(t, dir, "payload.bin", string(payload))
		in := harvestInput(dir, head, model.Card{
			ID: "c", Kind: model.KindDev, Acceptance: []string{"cat payload.bin; exit 1"},
		}, 1, nil, "")
		in.Home = home
		res, err := Harvest(context.Background(), in)
		if err != nil || res.EvidenceErr != nil {
			t.Fatalf("harvest %v evidence %v", err, res.EvidenceErr)
		}
		stored, err := os.ReadFile(filepath.Join(home, "attempts", "att", "acceptance.log"))
		if err != nil {
			t.Fatal(err)
		}
		headBytes, tailBytes, omitted := splitOmitted(t, stored)
		headerLen := bytes.IndexByte(stored, '\n') + 1
		if headerLen <= 1 {
			t.Fatal("missing header")
		}
		want := headerLen + len(payload) - 2*testEdge
		if omitted != want {
			t.Fatalf("omitted %d want %d header %d", omitted, want, headerLen)
		}
		if !bytes.Contains(headBytes, []byte("HEADMARK")) || !bytes.Contains(tailBytes, []byte("TAILMARK")) {
			t.Fatal("edge markers missing")
		}
		if bytes.Contains(stored, []byte("MIDMARK-GONE")) || bytes.Count(stored, []byte("\nomitted ")) != 1 {
			t.Fatal("middle marker survived or omitted line repeated")
		}
	})

	t.Run("result.md over 1 MiB names the omitted bytes", func(t *testing.T) {
		dir, head := newRepo(t)
		home := t.TempDir()
		body := []byte("## 事实\n")
		body = append(body, bytes.Repeat([]byte("A"), testLimit)...)
		copy(body[len(body)-8:], []byte("TAILMARK"))
		copy(body[80<<10:], []byte("MIDMARK-GONE"))
		write(t, dir, ".cardex-result.md", string(body))
		in := harvestInput(dir, head, model.Card{ID: "c", Kind: model.KindResearch}, 0, nil, "")
		in.Home = home
		res, err := Harvest(context.Background(), in)
		if err != nil || res.EvidenceErr != nil {
			t.Fatalf("harvest %v evidence %v", err, res.EvidenceErr)
		}
		stored, err := os.ReadFile(filepath.Join(home, "attempts", "att", "result.md"))
		if err != nil {
			t.Fatal(err)
		}
		headBytes, tailBytes, omitted := splitOmitted(t, stored)
		if omitted != len(body)-2*testEdge {
			t.Fatalf("omitted %d want %d", omitted, len(body)-2*testEdge)
		}
		if !bytes.Equal(headBytes, body[:testEdge]) || !bytes.Equal(tailBytes, body[len(body)-testEdge:]) {
			t.Fatal("result edges do not match the original")
		}
		if bytes.Contains(stored, []byte("MIDMARK-GONE")) {
			t.Fatal("middle marker survived")
		}
	})

	t.Run("result.md remains after the worktree is removed", func(t *testing.T) {
		dir, head := newRepo(t)
		home := t.TempDir()
		body := "## 复核结论\npass\nkept opinion\n"
		write(t, dir, ".cardex-result.md", body)
		in := harvestInput(dir, head, model.Card{ID: "c", Kind: model.KindReview}, 0, nil, "")
		in.Home = home
		in.Attempt.ID = "att-survive"
		if _, err := Harvest(context.Background(), in); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(home, "attempts", "att-survive", "result.md"))
		if err != nil || string(got) != body {
			t.Fatalf("result.md %q %v", got, err)
		}
	})
}

func assertTruncated(t *testing.T, in, stored []byte) {
	t.Helper()
	head, tail, omitted := splitOmitted(t, stored)
	if omitted != len(in)-2*testEdge {
		t.Fatalf("omitted %d want %d", omitted, len(in)-2*testEdge)
	}
	if !bytes.Equal(head, in[:testEdge]) || !bytes.Equal(tail, in[len(in)-testEdge:]) {
		t.Fatalf("edges head %d tail %d", len(head), len(tail))
	}
	if bytes.Count(stored, []byte("\nomitted ")) != 1 {
		t.Fatal("omitted line was not unique")
	}
}

func splitOmitted(t *testing.T, stored []byte) (head, tail []byte, omitted int) {
	t.Helper()
	if len(stored) < 2*testEdge {
		t.Fatalf("stored %d", len(stored))
	}
	tail = stored[len(stored)-testEdge:]
	prefix := stored[:len(stored)-testEdge]
	if !bytes.HasSuffix(prefix, []byte("\n")) {
		t.Fatal("omitted line has no newline")
	}
	body := prefix[:len(prefix)-1]
	nl := bytes.LastIndexByte(body, '\n')
	if nl < 0 {
		t.Fatal("no line before omitted")
	}
	line := string(body[nl+1:])
	if _, err := fmt.Sscanf(line, "omitted %d bytes", &omitted); err != nil || line != fmt.Sprintf("omitted %d bytes", omitted) {
		t.Fatalf("line %q", line)
	}
	rest := body[:nl+1]
	switch {
	case len(rest) == testEdge:
		head = rest
	case len(rest) == testEdge+1 && rest[testEdge] == '\n':
		head = rest[:testEdge]
	default:
		t.Fatalf("head shape %d", len(rest))
	}
	if bytes.Count(stored, []byte("\nomitted ")) != 1 {
		t.Fatal("omitted line was not unique")
	}
	return head, tail, omitted
}

func sizeOf(info os.FileInfo) int64 {
	if info == nil {
		return -1
	}
	return info.Size()
}
