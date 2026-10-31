package claims

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"cardex/internal/model"
)

func TestParseSymbols(t *testing.T) {
	src := []byte(`package p

const C = 1
var V = 2

type Server struct{}
type Alias = int

func Top() {}

func (s *Server) Run() {}

func (s Server) Stop() {}

func (b *Box[T]) Get() T { var z T; return z }
`)
	got, err := ParseSymbols("p.go", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Top", "Server.Run", "Server.Stop", "Box.Get", "Server", "Alias"} {
		if _, ok := got[name]; !ok {
			t.Fatalf("missing %s in %v", name, keys(got))
		}
	}
	if _, ok := got["C"]; ok {
		t.Fatal("const extracted")
	}
	if _, ok := got["V"]; ok {
		t.Fatal("var extracted")
	}
	if !strings.Contains(got["Server.Run"], "func (s *Server) Run()") {
		t.Fatalf("method source %q", got["Server.Run"])
	}
}

func TestChangedSymbols(t *testing.T) {
	repo := initClaimsRepo(t)
	writeFile(t, repo, "old.go", "package p\n\ntype S struct{}\n\nfunc Keep() {}\n\nfunc Old() int { return 1 }\n\nfunc (s *S) M() int { return 1 }\n")
	writeFile(t, repo, "note.txt", "one\n")
	gitClaims(t, repo, "add", "old.go", "note.txt")
	gitClaims(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "base")
	before := gitClaimsOut(t, repo, "rev-parse", "HEAD")

	writeFile(t, repo, "old.go", "package p\n\ntype S struct{}\n\nfunc Keep() {}\n\nfunc Old() int { return 2 }\n\nfunc New() {}\n\nfunc (s *S) M() int { return 3 }\n")
	writeFile(t, repo, "note.txt", "two\n")
	writeFile(t, repo, "brand.go", "package p\n\nfunc Brand() {}\n")
	gitClaims(t, repo, "add", "old.go", "note.txt", "brand.go")
	gitClaims(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "edit")
	after := gitClaimsOut(t, repo, "rev-parse", "HEAD")

	got, err := ChangedSymbols(repo, before, after)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.Claim{
		{Path: "brand.go"},
		{Path: "note.txt"},
		{Path: "old.go", Symbol: "New"},
		{Path: "old.go", Symbol: "Old"},
		{Path: "old.go", Symbol: "S.M"},
	}
	if !sameClaims(got, want) {
		t.Fatalf("claims %#v want %#v", got, want)
	}
	for _, c := range got {
		if c.Path == "brand.go" && c.Symbol != "" {
			t.Fatalf("new file claimed by symbol: %+v", c)
		}
	}
}

func TestConflictsFourCases(t *testing.T) {
	dev := model.KindDev

	t.Run("different funcs", func(t *testing.T) {
		got, err := Conflicts([]model.Card{
			{ID: "X", Kind: dev, Claims: []model.Claim{{Path: "f.go", Symbol: "Foo"}}},
			{ID: "Y", Kind: dev, Claims: []model.Claim{{Path: "f.go", Symbol: "Bar"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("conflicts %#v", got)
		}
	})

	t.Run("same func", func(t *testing.T) {
		got, err := Conflicts([]model.Card{
			{ID: "X", Kind: dev, Claims: []model.Claim{{Path: "f.go", Symbol: "Foo"}}},
			{ID: "Y", Kind: dev, Claims: []model.Claim{{Path: "f.go", Symbol: "Foo"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("conflicts %#v", got)
		}
		if got[0].Card != "Y" || got[0].Other != "X" || got[0].Path != "f.go" || got[0].Symbol != "Foo" {
			t.Fatalf("conflict %+v", got[0])
		}
		if got[0].Reason != "被卡 X 占用 f.go:Foo" {
			t.Fatalf("reason %q", got[0].Reason)
		}
	})

	t.Run("file level", func(t *testing.T) {
		got, err := Conflicts([]model.Card{
			{ID: "X", Kind: dev, Claims: []model.Claim{{Path: "f.go"}}},
			{ID: "Y", Kind: dev, Claims: []model.Claim{{Path: "f.go", Symbol: "Foo"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Reason != "被卡 X 占用 f.go:Foo" {
			t.Fatalf("conflicts %#v", got)
		}
	})

	t.Run("new file", func(t *testing.T) {
		got, err := Conflicts([]model.Card{
			{ID: "X", Kind: dev, Claims: []model.Claim{{Path: "new.go"}}},
			{ID: "Y", Kind: dev, Claims: []model.Claim{{Path: "new.go"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Reason != "被卡 X 占用 new.go" {
			t.Fatalf("conflicts %#v", got)
		}
	})
}

func TestConflictsDirectoryReviewAndEmpty(t *testing.T) {
	dev := model.KindDev
	got, err := Conflicts([]model.Card{
		{ID: "dir", Kind: dev, Claims: []model.Claim{{Path: "pkg/"}}},
		{ID: "sym", Kind: dev, Claims: []model.Claim{{Path: "pkg/exist.go", Symbol: "Foo"}}},
		{ID: "fresh", Kind: dev, Claims: []model.Claim{{Path: "pkg/new.go"}}},
		{ID: "review", Kind: model.KindReview, Claims: []model.Claim{{Path: "pkg/new.go"}}},
		{ID: "empty", Kind: dev},
		{ID: "other", Kind: dev, Claims: []model.Claim{{Path: "pkg/exist.go", Symbol: "Bar"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("conflicts %#v", got)
	}
	if got[0].Card != "fresh" || got[0].Other != "dir" || got[0].Path != "pkg/new.go" {
		t.Fatalf("conflict %+v", got[0])
	}
	outside, err := Conflicts([]model.Card{
		{ID: "dir", Kind: dev, Claims: []model.Claim{{Path: "pkg/"}}},
		{ID: "out", Kind: dev, Claims: []model.Claim{{Path: "other/new.go"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outside) != 0 {
		t.Fatalf("outside %#v", outside)
	}
}

func TestWorktreeSymbolsSkipsResultFile(t *testing.T) {
	repo := initClaimsRepo(t)
	writeFile(t, repo, "a.go", "package p\n\nfunc A() {}\n")
	gitClaims(t, repo, "add", "a.go")
	gitClaims(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "base")
	base := gitClaimsOut(t, repo, "rev-parse", "HEAD")
	writeFile(t, repo, "a.go", "package p\n\nfunc A() { println(1) }\n")
	writeFile(t, repo, ".cardex-result.md", "nope\n")
	got, err := WorktreeSymbols(context.Background(), repo, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != "a.go" || got[0].Symbol != "A" {
		t.Fatalf("claims %#v", got)
	}
}

func initClaimsRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitClaims(t, dir, "init", "-b", "main")
	return dir
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitClaims(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := gitClaimsOptional(dir, args...); err != nil {
		t.Fatal(err)
	}
}

func gitClaimsOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitClaimsOptional(dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func gitClaimsOptional(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", errString(args, out, err)
	}
	return strings.TrimSpace(string(out)), nil
}

type cmdErr struct{ msg string }

func (e *cmdErr) Error() string { return e.msg }

func errString(args []string, out []byte, err error) error {
	return &cmdErr{msg: strings.Join(args, " ") + ": " + err.Error() + ": " + strings.TrimSpace(string(out))}
}

func sameClaims(got, want []model.Claim) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].Path != want[i].Path || got[i].Symbol != want[i].Symbol {
			return false
		}
	}
	return true
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
