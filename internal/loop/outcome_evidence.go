package loop

import (
	"strings"
	"unicode/utf8"
)

// opinionLimit is the largest review opinion stored on a review_verdict event.
const opinionLimit = 64 * 1024

// reviewOpinion is the raw 「复核结论」 section stored on a review_verdict.
// Nested headings stay in the section. A missing section returns the result
// text unchanged. ReviewOf still supplies the verdict and ReworkNote.
func reviewOpinion(raw string) string {
	body, ok := sectionBodyNested(raw, "复核结论")
	if !ok {
		return raw
	}
	return body
}

// sectionBodyNested returns the body of the first heading named name,
// including deeper headings. The next heading at the same or higher level ends it.
// A fence of at least three ` or ~ (optional info string) hides headings until
// a line of only that same character at least as long as the opener. An unclosed
// fence runs through the rest of the text.
func sectionBodyNested(raw, name string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	start := -1
	level := 0
	var open fenceOpen
	for i, line := range lines {
		var skip bool
		open, skip = advanceFence(open, line)
		if skip {
			continue
		}
		lvl, title, ok := headingLevel(line)
		if !ok || normHeading(title) != normHeading(name) {
			continue
		}
		start = i + 1
		level = lvl
		break
	}
	if start < 0 {
		return "", false
	}
	end := len(lines)
	open = fenceOpen{}
	for i := start; i < len(lines); i++ {
		var skip bool
		open, skip = advanceFence(open, lines[i])
		if skip {
			continue
		}
		lvl, _, ok := headingLevel(lines[i])
		if ok && lvl <= level {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n"), true
}

// fenceOpen is an unmatched code fence. The zero value means none is open.
type fenceOpen struct {
	ch byte
	n  int
}

// advanceFence tracks an open fence by marker character and length.
// A trimmed line whose first run is three or more ` or ~ opens one.
// While open, heading detection skips every line, including the fence lines.
// A trimmed line of only the same character, at least as long as the opener
// and with no info string, closes it. A shorter run, the other character, or
// a line that still has an info string does not. The returned bool is true
// when heading detection must skip the line.
func advanceFence(open fenceOpen, line string) (fenceOpen, bool) {
	ch, n, rest, ok := leadingFence(line)
	if open.n > 0 {
		if ok && ch == open.ch && n >= open.n && strings.TrimSpace(rest) == "" {
			return fenceOpen{}, true
		}
		return open, true
	}
	if ok {
		return fenceOpen{ch: ch, n: n}, true
	}
	return fenceOpen{}, false
}

// leadingFence reports a run of at least three ` or ~ at the start of a trimmed line.
func leadingFence(line string) (ch byte, n int, rest string, ok bool) {
	trim := strings.TrimSpace(line)
	if trim == "" {
		return 0, 0, "", false
	}
	ch = trim[0]
	if ch != '`' && ch != '~' {
		return 0, 0, "", false
	}
	i := 0
	for i < len(trim) && trim[i] == ch {
		i++
	}
	if i < 3 {
		return 0, 0, "", false
	}
	return ch, i, trim[i:], true
}

func headingLevel(line string) (int, string, bool) {
	trim := strings.TrimSpace(line)
	if !strings.HasPrefix(trim, "#") {
		return 0, "", false
	}
	n := 0
	for n < len(trim) && trim[n] == '#' {
		n++
	}
	return n, trim[n:], true
}

// truncateOpinion keeps the opinion text and reports whether it was cut.
// A cut stops on a UTF-8 boundary. The stored card ReworkNote is not passed through here.
func truncateOpinion(s string) (string, bool) {
	if len(s) <= opinionLimit {
		return s, false
	}
	cut := opinionLimit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}
