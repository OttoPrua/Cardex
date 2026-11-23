package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/proc"
	"github.com/OttoPrua/Cardex/internal/store"
)

func plainVersion() string {
	return "cardex " + version + " " + buildRevision() + "\n"
}

func restoreTerm(t *testing.T) {
	t.Helper()
	prevTTY, prevBits := isTTY, colorBits
	t.Cleanup(func() {
		isTTY = prevTTY
		colorBits = prevBits
	})
	bannerWrites = 0
}

func withoutNoColor(t *testing.T) {
	t.Helper()
	orig, had := os.LookupEnv("NO_COLOR")
	os.Unsetenv("NO_COLOR")
	t.Cleanup(func() {
		if had {
			os.Setenv("NO_COLOR", orig)
		} else {
			os.Unsetenv("NO_COLOR")
		}
	})
}

func stripANSI(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		if in {
			if r == 'm' {
				in = false
			}
			continue
		}
		if r == 0x1b {
			in = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func TestVersionNonTTYAndJSONMatchPlainBytes(t *testing.T) {
	restoreTerm(t)
	withoutNoColor(t)
	isTTY = func(io.Writer) bool { return false }
	colorBits = func() int { return 24 }

	var out, errb bytes.Buffer
	if code := Main([]string{"version"}, &out, &errb); code != 0 {
		t.Fatalf("version exit %d %s", code, errb.String())
	}
	if out.String() != plainVersion() {
		t.Fatalf("non-TTY version\n got %q\nwant %q", out.String(), plainVersion())
	}
	if bannerWrites != 0 {
		t.Fatalf("banner writes off TTY: %d", bannerWrites)
	}

	out.Reset()
	isTTY = func(io.Writer) bool { return true }
	if code := Main([]string{"--json", "version"}, &out, &errb); code != 0 {
		t.Fatalf("json version exit %d", code)
	}
	if out.String() != plainVersion() {
		t.Fatalf("--json version\n got %q\nwant %q", out.String(), plainVersion())
	}
	if bannerWrites != 0 {
		t.Fatalf("banner writes with --json: %d", bannerWrites)
	}
}

func TestVersionNOCOLORSkipsBanner(t *testing.T) {
	restoreTerm(t)
	t.Setenv("NO_COLOR", "1")
	isTTY = func(io.Writer) bool { return true }
	colorBits = func() int { return 24 }
	var out, errb bytes.Buffer
	if code := Main([]string{"version"}, &out, &errb); code != 0 {
		t.Fatal(code)
	}
	if out.String() != plainVersion() {
		t.Fatalf("NO_COLOR version\n got %q\nwant %q", out.String(), plainVersion())
	}
	if bannerWrites != 0 {
		t.Fatalf("banner writes with NO_COLOR: %d", bannerWrites)
	}
}

func TestVersionBannerOnlyOnTTY(t *testing.T) {
	restoreTerm(t)
	withoutNoColor(t)
	calls := 0
	isTTY = func(io.Writer) bool {
		calls++
		return true
	}
	colorBits = func() int { return 24 }
	var out, errb bytes.Buffer
	if code := Main([]string{"version"}, &out, &errb); code != 0 {
		t.Fatal(code)
	}
	if calls == 0 {
		t.Fatal("isTTY was not consulted")
	}
	if bannerWrites != 1 {
		t.Fatalf("banner writes %d", bannerWrites)
	}
	got := out.String()
	if !strings.Contains(got, "you set the goal — cardex deals the cards") {
		t.Fatalf("slogan missing\n%s", got)
	}
	if !strings.Contains(got, "38;2;125;166;255") {
		t.Fatal("truecolor mark missing")
	}
	if !strings.HasSuffix(got, plainVersion()) {
		t.Fatalf("plain version line changed\n%q", got)
	}
}

func TestServeBannerGate(t *testing.T) {
	restoreTerm(t)
	withoutNoColor(t)
	line := "serve listening 127.0.0.1:9\n"
	home := t.TempDir()

	var plain bytes.Buffer
	isTTY = func(io.Writer) bool { return false }
	if _, err := serveLog(&plain, false, home).Write([]byte(line)); err != nil {
		t.Fatal(err)
	}
	if plain.String() != line || bannerWrites != 0 {
		t.Fatalf("off TTY %q writes %d", plain.String(), bannerWrites)
	}

	var jsonOut bytes.Buffer
	isTTY = func(io.Writer) bool { return true }
	if _, err := serveLog(&jsonOut, true, home).Write([]byte(line)); err != nil {
		t.Fatal(err)
	}
	if jsonOut.String() != line || bannerWrites != 0 {
		t.Fatalf("json serve log %q writes %d", jsonOut.String(), bannerWrites)
	}

	t.Setenv("NO_COLOR", "1")
	var no bytes.Buffer
	if _, err := serveLog(&no, false, home).Write([]byte(line)); err != nil {
		t.Fatal(err)
	}
	if no.String() != line || bannerWrites != 0 {
		t.Fatalf("NO_COLOR serve log %q writes %d", no.String(), bannerWrites)
	}
}

func TestServeBannerOnTTYKeepsListeningLine(t *testing.T) {
	restoreTerm(t)
	withoutNoColor(t)
	isTTY = func(io.Writer) bool { return true }
	colorBits = func() int { return 24 }
	home := t.TempDir()
	var buf bytes.Buffer
	line := "serve listening 127.0.0.1:9\n"
	w := serveLog(&buf, false, home)
	if _, ok := w.(*listeningBanner); !ok {
		t.Fatalf("TTY serve log type %T", w)
	}
	if _, err := io.WriteString(w, line+"serve loop iteration 1\n"); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if bannerWrites != 1 {
		t.Fatalf("writes %d", bannerWrites)
	}
	if !strings.Contains(got, "cardex "+version+" · serve") {
		t.Fatalf("serve banner\n%s", got)
	}
	if !strings.Contains(got, "board  \x1b[38;2;47;91;234mhttp://127.0.0.1:9") {
		t.Fatalf("board url\n%s", got)
	}
	if !strings.HasSuffix(got, line+"serve loop iteration 1\n") {
		t.Fatalf("listening bytes changed\n%q", got)
	}
}

func TestStatusPlainJSONAndColorBytes(t *testing.T) {
	restoreTerm(t)
	withoutNoColor(t)
	home, obj := seedManualNodeHome(t)
	withDB(t, home, func(db *store.Store) {
		node := model.DesignNode{ID: "n1", Objective: obj, Layer: model.LayerModule, Status: model.NodeFrozen}
		if err := db.CreateNode(context.Background(), &node); err != nil {
			t.Fatal(err)
		}
		seedNodeCard(t, db, "c1", obj, "n1", model.KindDev, model.CardRunning)
	})

	isTTY = func(io.Writer) bool { return false }
	code, nodePlain, errb := callCLI(t, []string{"--home", home, "node", "list"})
	if code != 0 || nodePlain != "n1\tfrozen\n" {
		t.Fatalf("node list %d %q %s", code, nodePlain, errb)
	}
	_, jsonPlain, errb := callCLI(t, []string{"--home", home, "--json", "node", "list"})
	if errb != "" && jsonPlain == "" {
		t.Fatal(errb)
	}
	code, showPlain, errb := callCLI(t, []string{"--home", home, "card", "show", "c1"})
	if code != 0 || showPlain != "c1\tc1\n" {
		t.Fatalf("card show %d %q %s", code, showPlain, errb)
	}
	code, pausePlain, errb := callCLI(t, []string{"--home", home, "card", "pause", "c1"})
	if code != 0 || pausePlain != "c1\tpaused\n" {
		t.Fatalf("card pause %d %q %s", code, pausePlain, errb)
	}

	isTTY = func(io.Writer) bool { return true }
	colorBits = func() int { return 24 }
	_, jsonTTY, _ := callCLI(t, []string{"--home", home, "--json", "node", "list"})
	if jsonTTY != jsonPlain || strings.Contains(jsonTTY, "\x1b") {
		t.Fatalf("json bytes changed")
	}
	_, showTTY, _ := callCLI(t, []string{"--home", home, "card", "show", "c1"})
	if showTTY != showPlain {
		t.Fatalf("card show tty %q plain %q", showTTY, showPlain)
	}
	_, pauseTTY, _ := callCLI(t, []string{"--home", home, "card", "pause", "c1"})
	if stripANSI(pauseTTY) != "c1\tpaused\n" {
		t.Fatalf("pause text %q", stripANSI(pauseTTY))
	}
	if !strings.Contains(pauseTTY, "48;2;163;158;144") || !strings.Contains(pauseTTY, "paused") {
		t.Fatalf("pause color\n%s", pauseTTY)
	}
	if bannerWrites != 0 {
		t.Fatalf("status commands wrote a banner: %d", bannerWrites)
	}
}

func TestStatusColorFallbacks(t *testing.T) {
	restoreTerm(t)
	withoutNoColor(t)
	isTTY = func(io.Writer) bool { return true }
	buf := &bytes.Buffer{}

	colorBits = func() int { return 24 }
	got := paintStatus(buf, false, "running")
	if !strings.Contains(got, "48;2;47;91;234") || !strings.Contains(got, "running") {
		t.Fatalf("truecolor %q", got)
	}
	colorBits = func() int { return 256 }
	got = paintStatus(buf, false, "running")
	if !strings.Contains(got, "48;5;33") || strings.Contains(got, "48;2;") {
		t.Fatalf("256 %q", got)
	}
	colorBits = func() int { return 16 }
	got = paintStatus(buf, false, "running")
	if !strings.Contains(got, "44;97") || strings.Contains(got, "48;2;") {
		t.Fatalf("16 %q", got)
	}
	if paintStatus(buf, false, "not-a-status") != "not-a-status" {
		t.Fatal("unknown status was painted")
	}
	if paintStatus(buf, true, "running") != "running" {
		t.Fatal("json status was painted")
	}

	mark := markFor(256)
	if strings.Contains(mark, "38;2;") || !strings.Contains(mark, "38;5;111") {
		t.Fatal("256 mark fallback")
	}
	mark = markFor(16)
	if strings.Contains(mark, "38;2;") || !strings.Contains(mark, "\x1b[94m") {
		t.Fatal("16 mark fallback")
	}
}

func TestVersionBannerHomeMatchesServe(t *testing.T) {
	restoreTerm(t)
	withoutNoColor(t)
	isTTY = func(io.Writer) bool { return true }
	colorBits = func() int { return 24 }

	homeRoot := t.TempDir()
	profileRoot := t.TempDir()
	t.Setenv("HOME", homeRoot)
	t.Setenv("USERPROFILE", profileRoot)
	t.Setenv("CARDEX_HOME", "")

	got := peekHome("")
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Fatalf("peekHome created %s: %v", got, err)
	}
	user, err := proc.UserHome()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(filepath.Join(user, ".cardex"))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("peekHome %s, serve default %s", got, want)
	}
	if show := displayHome(got); show != "~"+string(os.PathSeparator)+".cardex" {
		t.Fatalf("displayHome %q", show)
	}
	resolved, err := resolveHome("")
	if err != nil {
		t.Fatal(err)
	}
	if got != resolved {
		t.Fatalf("banner home %s, serve home %s", got, resolved)
	}

	if err := os.WriteFile(filepath.Join(got, "serve-addr"), []byte("127.0.0.1:4242\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(profileRoot, ".cardex")
	if err := os.MkdirAll(decoy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoy, "serve-addr"), []byte("10.9.8.7:1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := cmdVersion("", false, &out); code != 0 {
		t.Fatalf("version %d", code)
	}
	text := out.String()
	if !strings.Contains(text, "http://127.0.0.1:4242") {
		t.Fatalf("banner missed the serve address\n%s", text)
	}
	if strings.Contains(text, "10.9.8.7") {
		t.Fatalf("banner read USERPROFILE home\n%s", text)
	}
	if !strings.HasSuffix(text, plainVersion()) {
		t.Fatalf("plain version line changed\n%q", text)
	}
	if runtime.GOOS == "windows" {
		u, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Clean(u) == filepath.Clean(homeRoot) {
			t.Fatal("os.UserHomeDir followed HOME; Windows split was not set up")
		}
		if strings.HasPrefix(filepath.Clean(got), filepath.Clean(profileRoot)) {
			t.Fatalf("peekHome used USERPROFILE %s", got)
		}
	}

	t.Setenv("CARDEX_HOME", filepath.Join(profileRoot, "from-env"))
	envAbs, err := filepath.Abs(filepath.Join(profileRoot, "from-env"))
	if err != nil {
		t.Fatal(err)
	}
	if envHome := peekHome(""); envHome != envAbs {
		t.Fatalf("CARDEX_HOME %s want %s", envHome, envAbs)
	}
	if _, err := os.Stat(envAbs); !os.IsNotExist(err) {
		t.Fatalf("CARDEX_HOME peek created %s: %v", envAbs, err)
	}
	flag := filepath.Join(homeRoot, "flag-home")
	flagAbs, err := filepath.Abs(flag)
	if err != nil {
		t.Fatal(err)
	}
	if peekHome(flag) != flagAbs {
		t.Fatalf("flag home %s", peekHome(flag))
	}
	if _, err := os.Stat(flagAbs); !os.IsNotExist(err) {
		t.Fatalf("--home peek created %s: %v", flagAbs, err)
	}
}
