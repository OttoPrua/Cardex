package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cardex/internal/store"
)

func TestDefaultHomeIsCreatedUnderTempHOME(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("CARDEX_HOME", "")
	code, out, errb := runMain(t, "project", "list")
	if code != 0 {
		t.Fatalf("project list %d\n%s\n%s", code, out, errb)
	}
	dir := filepath.Join(root, ".cardex")
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("default home: %v", err)
	}
	if !filepath.IsAbs(dir) {
		t.Fatalf("default home %s is not absolute", dir)
	}
	got, err := resolveHome("")
	if err != nil || got != dir {
		t.Fatalf("resolveHome %s want %s err %v", got, dir, err)
	}
}

func TestHomeFlagAndEnvOverrideDefault(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	envHome := t.TempDir()
	flagHome := t.TempDir()
	t.Setenv("CARDEX_HOME", envHome)
	got, err := resolveHome("")
	if err != nil || got != envHome {
		t.Fatalf("CARDEX_HOME %s err %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".cardex")); !os.IsNotExist(err) {
		t.Fatal("default home created while CARDEX_HOME is set")
	}
	got, err = resolveHome(flagHome)
	if err != nil || got != flagHome {
		t.Fatalf("--home %s err %v", got, err)
	}
	code, out, errb := runMain(t, "--home", flagHome, "project", "list")
	if code != 0 {
		t.Fatalf("flag home %d\n%s\n%s", code, out, errb)
	}
}

func TestCLIRefusesV1Home(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CARDEX_HOME", "")
	dir := makeV1(t)
	code, _, errb := runMain(t, "--home", dir, "project", "list")
	if code == 0 || !strings.Contains(errb, "import-legacy") || !strings.Contains(errb, "v1") {
		t.Fatalf("code %d err %s", code, errb)
	}
	if _, err := os.Stat(filepath.Join(dir, "cardex.db")); !os.IsNotExist(err) {
		t.Fatal("command created cardex.db")
	}
}

func TestCLIAllowsEmptyHomeAndTasksWithDB(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CARDEX_HOME", "")
	empty := t.TempDir()
	code, out, errb := runMain(t, "--home", empty, "project", "list")
	if code != 0 {
		t.Fatalf("empty %d\n%s\n%s", code, out, errb)
	}

	dir := t.TempDir()
	code, out, errb = runMain(t, "--home", dir, "project", "list")
	if code != 0 {
		t.Fatalf("seed db %d\n%s\n%s", code, out, errb)
	}
	writeTaskFile(t, dir)
	code, out, errb = runMain(t, "--home", dir, "project", "list")
	if code != 0 {
		t.Fatalf("tasks plus cardex.db %d\n%s\n%s", code, out, errb)
	}
}

func TestCLIAllowsChildOfV1Home(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CARDEX_HOME", "")
	parent := makeV1(t)
	child := filepath.Join(parent, "child")
	code, out, errb := runMain(t, "--home", child, "project", "list")
	if code != 0 {
		t.Fatalf("child %d\n%s\n%s", code, out, errb)
	}
	if _, err := os.Stat(filepath.Join(parent, "cardex.db")); !os.IsNotExist(err) {
		t.Fatal("parent gained cardex.db")
	}
}

func TestImportLegacyFromV1Source(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CARDEX_HOME", "")
	src := makeV1(t)
	before := readFile(t, filepath.Join(src, "tasks", "a.json"))
	dst := t.TempDir()
	code, out, errb := runMain(t, "--home", dst, "import-legacy", src)
	if code != 0 {
		t.Fatalf("import %d\n%s\n%s", code, out, errb)
	}
	if !strings.Contains(out, "v1 queued") {
		t.Fatalf("report %s", out)
	}
	if got := readFile(t, filepath.Join(src, "tasks", "a.json")); got != before {
		t.Fatal("source bytes changed")
	}
	db, err := store.Open(filepath.Join(dst, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cards, err := db.ListCards(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) == 0 {
		t.Fatal("import wrote no cards")
	}
}

func TestImportLegacyRefusesV1Target(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CARDEX_HOME", "")
	src := makeV1(t)
	before := readFile(t, filepath.Join(src, "tasks", "a.json"))
	dst := makeV1(t)
	code, _, errb := runMain(t, "--home", dst, "import-legacy", src)
	if code == 0 || !strings.Contains(errb, "import-legacy") {
		t.Fatalf("code %d err %s", code, errb)
	}
	if _, err := os.Stat(filepath.Join(dst, "cardex.db")); !os.IsNotExist(err) {
		t.Fatal("v1 target gained cardex.db")
	}
	if got := readFile(t, filepath.Join(src, "tasks", "a.json")); got != before {
		t.Fatal("source bytes changed")
	}
}

func TestImportLegacySameDirectoryErrorUnchanged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CARDEX_HOME", "")
	dir := t.TempDir()
	code, _, errb := runMain(t, "--home", dir, "import-legacy", dir)
	if code == 0 || !strings.Contains(errb, "v2 home and v1 home are the same directory") {
		t.Fatalf("code %d err %s", code, errb)
	}
}

func TestDoctorSuggestions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CARDEX_HOME", "")
	v1 := makeV1(t)
	code, out, errb := runMain(t, "--home", v1, "doctor")
	if code != 0 {
		t.Fatalf("doctor %d\n%s\n%s", code, out, errb)
	}
	if strings.Contains(out, "outside ~/.cardex") || strings.Contains(errb, "outside ~/.cardex") {
		t.Fatalf("old path advice\n%s\n%s", out, errb)
	}
	if !strings.Contains(out, "import-legacy") || !strings.Contains(out, "新目录") {
		t.Fatalf("v1 suggestion missing\n%s", out)
	}

	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errb = runMain(t, "--home", file, "doctor")
	if code != 0 {
		t.Fatalf("doctor file %d\n%s\n%s", code, out, errb)
	}
	if strings.Contains(out, "outside ~/.cardex") || strings.Contains(out, "import-legacy") {
		t.Fatalf("non-v1 suggestion\n%s", out)
	}
	if !strings.Contains(out, "pass a writable --home") {
		t.Fatalf("generic suggestion missing\n%s", out)
	}
}

func TestServeAddrIsNotPortRefused(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CARDEX_HOME", "")
	// The v1 error quotes this path. Digits in the path are not a port refusal.
	parent := filepath.Join(t.TempDir(), "x8787y8788")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	v1 := filepath.Join(parent, "home")
	writeTaskFile(t, v1)
	cases := []struct {
		addr string
	}{
		{addr: "127.0.0.1:8787"},
		{addr: "127.0.0.1:8788"},
	}
	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			code, out, errb := runServeText(t, v1, tc.addr)
			if code == 0 {
				t.Fatalf("exited 0\n%s", out)
			}
			if !strings.Contains(errb, "import-legacy") {
				t.Fatalf("stderr %s", errb)
			}
			reported := strings.ReplaceAll(errb, v1, "")
			if strings.Contains(reported, "reserved") || strings.Contains(reported, "8787") || strings.Contains(reported, "8788") {
				t.Fatalf("port refusal %s", errb)
			}
			if _, err := os.Stat(filepath.Join(v1, "serve-addr")); !os.IsNotExist(err) {
				t.Fatal("listened")
			}
			t.Logf("exit %d stderr %s", code, errb)
		})
	}
	code, out, errb := runMain(t, "serve", "--help")
	text := out + errb
	if code != 0 {
		t.Fatalf("help %d\n%s", code, text)
	}
	if !strings.Contains(text, "listen address") || strings.Contains(text, "8787") || strings.Contains(text, "8788") || strings.Contains(text, "refused") {
		t.Fatalf("help text\n%s", text)
	}
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "reservedPort") || strings.Contains(string(body), "refusing to use ~/.cardex") {
		t.Fatal("old refusal remains in main.go")
	}
}

func runServeText(t *testing.T, home, addr string) (int, string, string) {
	t.Helper()
	var out, errb strings.Builder
	code := runServe(context.Background(), home, addr, &out, &errb)
	return code, out.String(), errb.String()
}

func runMain(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb strings.Builder
	code := Main(args, &out, &errb)
	return code, out.String(), errb.String()
}

func makeV1(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeTaskFile(t, dir)
	return dir
}

func writeTaskFile(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"id":"a","title":"Queue the lamp","status":"queued","project":"northwind"}`)
	if err := os.WriteFile(filepath.Join(dir, "tasks", "a.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
