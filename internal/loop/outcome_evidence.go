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
func sectionBodyNested(raw, name string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	start := -1
	level := 0
	for i, line := range lines {
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
	for i := start; i < len(lines); i++ {
		lvl, _, ok := headingLevel(lines[i])
		if ok && lvl <= level {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n"), true
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
