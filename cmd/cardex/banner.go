package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/OttoPrua/Cardex/internal/proc"
)

// isTTY reports whether w is the process stdout and a terminal.
// Tests replace it. Buffers and pipes are not terminals, so their bytes stay plain.
var isTTY = stdoutIsTTY

// colorBits is 24, 256, or 16. Tests replace it.
var colorBits = detectColorBits

// bannerWrites counts writeBanner calls so tests can see the TTY branch.
var bannerWrites int

func stdoutIsTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok || f != os.Stdout {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func detectColorBits() int {
	ct := os.Getenv("COLORTERM")
	if ct == "truecolor" || ct == "24bit" {
		return 24
	}
	term := os.Getenv("TERM")
	switch {
	case strings.Contains(term, "truecolor"), strings.Contains(term, "24bit"), strings.Contains(term, "direct"):
		return 24
	case strings.Contains(term, "256"):
		return 256
	case term == "" || term == "dumb":
		if runtime.GOOS == "windows" {
			return 24
		}
		return 16
	default:
		return 16
	}
}

func noColor() bool {
	_, ok := os.LookupEnv("NO_COLOR")
	return ok
}

// useColor is the gate for the banner and for status-word colour.
// --json, NO_COLOR, and a non-terminal stdout all leave output unchanged.
func useColor(w io.Writer, asJSON bool) bool {
	if asJSON || noColor() {
		return false
	}
	return isTTY(w)
}

// cliMark is the half-block wordmark (16 columns, 7 rows, truecolor).
const cliMark = `   [38;2;125;166;255m▄[0m[38;2;125;166;255m▀[0m[38;2;125;166;255m▀[0m[38;2;125;166;255m▀[0m[38;2;125;166;255m▀[0m[38;2;125;166;255m▀[0m[38;2;125;166;255m▀[0m[38;2;125;166;255m▀[0m[38;2;125;166;255m▀[0m[38;2;125;166;255m▀[0m[38;2;125;166;255m▄[0m` + " \n" + ` [38;2;245;244;239m▄[0m[38;2;245;244;239m▄[0m[38;2;245;244;239m▄[0m[38;2;245;244;239m▄[0m[38;2;245;244;239m▄[0m[38;2;245;244;239m▄[0m[38;2;245;244;239m▄[0m[38;2;245;244;239m▄[0m[38;2;245;244;239m▄[0m[38;2;245;244;239m▄[0m   [38;2;125;166;255m[48;2;125;166;255m▀[0m
[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m  [38;2;125;166;255m[48;2;125;166;255m▀[0m
[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m  [38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m  [38;2;125;166;255m[48;2;125;166;255m▀[0m
[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m  [38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m▀[0m[38;2;245;244;239m▀[0m[38;2;245;244;239m▀[0m[38;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m [38;2;125;166;255m▄[0m[38;2;125;166;255m▀[0m
[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m[38;2;245;244;239m[48;2;245;244;239m▀[0m` + "   \n" + ` [38;2;245;244;239m▀[0m[38;2;245;244;239m▀[0m[38;2;245;244;239m▀[0m[38;2;245;244;239m▀[0m[38;2;245;244;239m▀[0m[38;2;245;244;239m▀[0m[38;2;245;244;239m▀[0m[38;2;245;244;239m▀[0m[38;2;245;244;239m▀[0m[38;2;245;244;239m▀[0m` + "    \n" + ``

type bannerView struct {
	Mode    string
	Version string
	Rev     string
	Home    string
	Addr    string
}

func writeBanner(w io.Writer, v bannerView) error {
	bannerWrites++
	bits := normalizeBits(colorBits())
	markLines := strings.Split(strings.TrimRight(markFor(bits), "\n"), "\n")
	width := 0
	for _, line := range markLines {
		if n := visibleWidth(line); n > width {
			width = n
		}
	}
	texts := bannerLines(v, bits)
	n := len(markLines)
	if len(texts) > n {
		n = len(texts)
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		mark := ""
		if i < len(markLines) {
			mark = markLines[i]
		}
		b.WriteString(mark)
		b.WriteString(strings.Repeat(" ", width-visibleWidth(mark)))
		if i < len(texts) {
			b.WriteString("  ")
			b.WriteString(texts[i])
		}
		b.WriteString("\033[0m\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func bannerLines(v bannerView, bits int) []string {
	ver := v.Version
	if ver == "" {
		ver = version
	}
	mode := bannerRev(v.Rev)
	if v.Mode == "serve" {
		mode = "serve"
	}
	lines := []string{
		fmt.Sprintf("\033[1mcardex %s · %s\033[0m", ver, mode),
		sgrSlogan(bits) + "you set the goal — cardex deals the cards\033[0m",
	}
	if url := boardURL(v.Addr); url != "" {
		line := "board  " + sgrBrand(bits) + url + "\033[0m"
		if home := displayHome(v.Home); home != "" {
			line += "   home  " + home
		}
		lines = append(lines, line)
	}
	return lines
}

func normalizeBits(bits int) int {
	switch bits {
	case 16, 256:
		return bits
	default:
		return 24
	}
}

func sgrBrand(bits int) string {
	switch bits {
	case 256:
		return "\033[38;5;27m"
	case 16:
		return "\033[34m"
	default:
		return "\033[38;2;47;91;234m"
	}
}

func sgrSlogan(bits int) string {
	switch bits {
	case 256:
		return "\033[4;38;5;27m"
	case 16:
		return "\033[4;34m"
	default:
		return "\033[4;38;2;47;91;234m"
	}
}

func markFor(bits int) string {
	mark := cliMark
	switch normalizeBits(bits) {
	case 256:
		mark = strings.ReplaceAll(mark, "38;2;125;166;255", "38;5;111")
		mark = strings.ReplaceAll(mark, "48;2;125;166;255", "48;5;111")
		mark = strings.ReplaceAll(mark, "38;2;245;244;239", "38;5;231")
		mark = strings.ReplaceAll(mark, "48;2;245;244;239", "48;5;231")
		return mark
	case 16:
		// 16-color sequences are substituted below.
	default:
		return mark
	}
	mark = strings.ReplaceAll(mark, "38;2;125;166;255", "94")
	mark = strings.ReplaceAll(mark, "48;2;125;166;255", "104")
	mark = strings.ReplaceAll(mark, "38;2;245;244;239", "97")
	mark = strings.ReplaceAll(mark, "48;2;245;244;239", "107")
	return mark
}

// bannerRev keeps the title line short. The plain version line still prints the full revision.
func bannerRev(rev string) string {
	if strings.HasPrefix(rev, "unknown ") {
		return "unknown"
	}
	return rev
}

func boardURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if strings.Contains(addr, "://") {
		return addr
	}
	return "http://" + addr
}

func displayHome(path string) string {
	if path == "" {
		return ""
	}
	u, err := proc.UserHome()
	if err == nil && u != "" && (path == u || strings.HasPrefix(path, u+string(os.PathSeparator))) {
		return "~" + strings.TrimPrefix(path, u)
	}
	return path
}

// peekHome is the directory serve would use, without creating it.
// CARDEX_HOME or an explicit home wins; otherwise proc.UserHome, the same
// choice as resolveHome. On Windows os.UserHomeDir reads only USERPROFILE,
// so a set HOME must not send the banner to a different directory.
func peekHome(home string) string {
	if home == "" {
		home = os.Getenv("CARDEX_HOME")
	}
	if home == "" {
		u, err := proc.UserHome()
		if err != nil {
			return ""
		}
		home = filepath.Join(u, ".cardex")
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return home
	}
	return abs
}

func loadServeAddr(home string) string {
	if home == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(home, "serve-addr"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func visibleWidth(s string) int {
	w := 0
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
		w++
	}
	return w
}

// serveLog passes sched's listening line through unchanged unless colour is on,
// in which case the banner is written once ahead of that line.
func serveLog(w io.Writer, asJSON bool, home string) io.Writer {
	if !useColor(w, asJSON) {
		return w
	}
	return &listeningBanner{w: w, home: home}
}

type listeningBanner struct {
	w    io.Writer
	home string
	buf  []byte
	sent bool
}

func (b *listeningBanner) Write(p []byte) (int, error) {
	if b.sent {
		return b.w.Write(p)
	}
	b.buf = append(b.buf, p...)
	i := indexByte(b.buf, '\n')
	if i < 0 {
		return len(p), nil
	}
	line := string(b.buf[:i])
	rest := append([]byte(nil), b.buf[i+1:]...)
	b.buf = nil
	b.sent = true
	if addr, ok := strings.CutPrefix(line, "serve listening "); ok {
		if err := writeBanner(b.w, bannerView{
			Mode:    "serve",
			Version: version,
			Rev:     buildRevision(),
			Home:    b.home,
			Addr:    addr,
		}); err != nil {
			return 0, err
		}
	}
	if _, err := io.WriteString(b.w, line+"\n"); err != nil {
		return 0, err
	}
	if len(rest) == 0 {
		return len(p), nil
	}
	if _, err := b.w.Write(rest); err != nil {
		return 0, err
	}
	return len(p), nil
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

type chip struct {
	bgR, bgG, bgB int
	fgR, fgG, fgB int
	bg256, fg256  int
	sgr16         string
}

func (c chip) wrap(text string, bits int) string {
	switch normalizeBits(bits) {
	case 256:
		return fmt.Sprintf("\033[1;38;5;%d;48;5;%dm%s\033[0m", c.fg256, c.bg256, text)
	case 16:
		return fmt.Sprintf("\033[1;%sm%s\033[0m", c.sgr16, text)
	default:
		return fmt.Sprintf("\033[1;38;2;%d;%d;%d;48;2;%d;%d;%dm%s\033[0m", c.fgR, c.fgG, c.fgB, c.bgR, c.bgG, c.bgB, text)
	}
}

func chipRGB(bg, fg string, bg256, fg256 int, sgr16 string) chip {
	br, bgc, bb := hexRGB(bg)
	fr, fgG, fb := hexRGB(fg)
	return chip{br, bgc, bb, fr, fgG, fb, bg256, fg256, sgr16}
}

func hexRGB(h string) (int, int, int) {
	h = strings.TrimPrefix(h, "#")
	var r, g, b int
	fmt.Sscanf(h, "%02x%02x%02x", &r, &g, &b)
	return r, g, b
}

func statusChips() map[string]chip {
	queued := chipRGB("#C9C4B5", "#16161A", 250, 16, "47;30")
	waiting := chipRGB("#E6E2D6", "#16161A", 250, 16, "47;30")
	paused := chipRGB("#A39E90", "#16161A", 250, 16, "47;30")
	canceled := chipRGB("#DFDBCF", "#16161A", 250, 16, "47;30")
	running := chipRGB("#2F5BEA", "#FFFFFF", 33, 15, "44;97")
	testing := chipRGB("#22B8CF", "#16161A", 37, 16, "46;30")
	review := chipRGB("#FFC53D", "#16161A", 220, 16, "43;30")
	yourCall := chipRGB("#FF7AB6", "#16161A", 211, 16, "45;30")
	mergeQ := chipRGB("#9EE6C4", "#16161A", 121, 16, "42;30")
	merged := chipRGB("#1FAE72", "#16161A", 35, 16, "42;30")
	failed := chipRGB("#FF5A36", "#16161A", 203, 16, "41;97")
	return map[string]chip{
		"ready":          queued,
		"waiting":        waiting,
		"paused":         paused,
		"canceled":       canceled,
		"running":        running,
		"harvesting":     testing,
		"reviewing":      review,
		"needs_input":    yourCall,
		"merge_queued":   mergeQ,
		"merged":         merged,
		"done":           merged,
		"failed":         failed,
		"active":         running,
		"done_candidate": review,
		"drafting":       queued,
		"freezable":      queued,
		"frozen":         paused,
		"reopened":       running,
	}
}

// paintStatus colours one status field. Unknown words and plain stdout are returned unchanged.
func paintStatus(w io.Writer, asJSON bool, status string) string {
	if !useColor(w, asJSON) {
		return status
	}
	return colorStatus(status)
}

func colorStatus(status string) string {
	c, ok := statusChips()[status]
	if !ok {
		return status
	}
	return c.wrap(status, colorBits())
}
