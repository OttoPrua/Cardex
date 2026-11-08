package board

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveHomeUsesCLIOrder(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	def := filepath.Join(root, ".cardex")
	envHome := t.TempDir()
	flagHome := t.TempDir()
	old := os.Args
	t.Cleanup(func() { os.Args = old })

	cases := []struct {
		name string
		args []string
		env  string
		want string
	}{
		{name: "default", args: []string{"cardex"}, env: "", want: def},
		{name: "env", args: []string{"cardex"}, env: envHome, want: envHome},
		{name: "flag", args: []string{"cardex", "--home", flagHome}, env: envHome, want: flagHome},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			os.Args = tc.args
			t.Setenv("CARDEX_HOME", tc.env)
			got, err := resolveHome()
			if err != nil || got != tc.want {
				t.Fatalf("resolveHome %s want %s err %v", got, tc.want, err)
			}
		})
	}
	if _, err := os.Stat(def); !os.IsNotExist(err) {
		t.Fatal("board created the default home")
	}
}

func TestResolveHomeRefusesV1DefaultHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("CARDEX_HOME", "")
	old := os.Args
	os.Args = []string{"cardex", "serve"}
	t.Cleanup(func() { os.Args = old })

	dir := filepath.Join(root, ".cardex")
	if err := os.MkdirAll(filepath.Join(dir, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tasks", "a.json"), []byte(`{"id":"a"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveHome(); err == nil || !strings.Contains(err.Error(), "import-legacy") {
		t.Fatalf("v1 default: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cardex.db")); !os.IsNotExist(err) {
		t.Fatal("board created cardex.db")
	}
	if _, err := os.Stat(filepath.Join(dir, "serve-addr")); !os.IsNotExist(err) {
		t.Fatal("v1 default listened")
	}
}

func TestResolveHomeRefusesV1AndAllowsDB(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := os.Args
	os.Args = []string{"cardex"}
	t.Cleanup(func() { os.Args = old })

	v1 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(v1, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v1, "tasks", "a.json"), []byte(`{"id":"a"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CARDEX_HOME", v1)
	if _, err := resolveHome(); err == nil || !strings.Contains(err.Error(), "import-legacy") {
		t.Fatalf("v1 home: %v", err)
	}
	if _, err := os.Stat(filepath.Join(v1, "cardex.db")); !os.IsNotExist(err) {
		t.Fatal("board created cardex.db")
	}

	if err := os.WriteFile(filepath.Join(v1, "cardex.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveHome()
	if err != nil || got != v1 {
		t.Fatalf("tasks plus cardex.db: %s %v", got, err)
	}
}

func TestResolveHomeDoesNotCreateAndAllowsNamedCardex(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	old := os.Args
	os.Args = []string{"cardex"}
	t.Cleanup(func() { os.Args = old })

	missing := filepath.Join(t.TempDir(), "not-yet")
	t.Setenv("CARDEX_HOME", missing)
	got, err := resolveHome()
	if err != nil || got != missing {
		t.Fatalf("missing dir: %s %v", got, err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("board created the home")
	}

	named := filepath.Join(root, ".cardex")
	if err := os.MkdirAll(named, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CARDEX_HOME", named)
	got, err = resolveHome()
	if err != nil || got != named {
		t.Fatalf("named .cardex: %s %v", got, err)
	}

	flagHome := t.TempDir()
	os.Args = []string{"cardex", "--home", flagHome}
	t.Setenv("CARDEX_HOME", named)
	got, err = resolveHome()
	if err != nil || got != flagHome {
		t.Fatalf("flag home: %s %v", got, err)
	}
}
