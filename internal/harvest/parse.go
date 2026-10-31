package harvest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode"
)

var headingRE = regexp.MustCompile(`(?m)^(#{1,6})[ \t]*(.*\S)[ \t]*$`)

var knownHeadings = map[string]struct{}{
	"结论":      {},
	"开放问题":    {},
	"事实":      {},
	"设计问题":    {},
	"新卡":      {},
	"复核结论":    {},
	"backlog": {},
}

type section struct {
	name string
	body string
}

func readResult(path string) (string, resultState, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", resultMissing, nil
		}
		return "", resultMissing, err
	}
	raw := string(body)
	if err := validateResult(raw); err != nil {
		return raw, resultUnparsable, nil
	}
	return raw, resultValid, nil
}

func validateResult(raw string) error {
	text := strings.TrimPrefix(raw, "\uFEFF")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	sections := splitSections(text)
	known := 0
	for _, s := range sections {
		if _, ok := knownHeadings[s.name]; !ok {
			continue
		}
		known++
		switch s.name {
		case "新卡":
			if err := parseLooseJSON(s.body); err != nil {
				return err
			}
		case "复核结论":
			if _, ok := reviewVerdict(s.body); !ok {
				return fmt.Errorf("review verdict")
			}
		}
	}
	if known == 0 {
		return fmt.Errorf("no known section")
	}
	return nil
}

func splitSections(raw string) []section {
	idx := headingRE.FindAllStringSubmatchIndex(raw, -1)
	if len(idx) == 0 {
		return nil
	}
	out := make([]section, 0, len(idx))
	for i, m := range idx {
		title := raw[m[4]:m[5]]
		bodyStart := m[1]
		bodyEnd := len(raw)
		if i+1 < len(idx) {
			bodyEnd = idx[i+1][0]
		}
		out = append(out, section{name: normHeading(title), body: raw[bodyStart:bodyEnd]})
	}
	return out
}

func normHeading(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if !unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func reviewVerdict(body string) (string, bool) {
	for _, field := range strings.Fields(strings.ToLower(body)) {
		field = strings.Trim(field, "`*_.,;:()[]\"'")
		if field == "pass" || field == "rework" {
			return field, true
		}
	}
	return "", false
}

func parseLooseJSON(s string) error {
	s = stripFences(s)
	s = stripJSONComments(s)
	s = stripTrailingCommas(s)
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("empty json")
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing json")
	}
	switch v.(type) {
	case map[string]any, []any:
		return nil
	default:
		return fmt.Errorf("json must be object or array")
	}
}

func stripFences(s string) string {
	s = strings.TrimSpace(s)
	var fence string
	switch {
	case strings.HasPrefix(s, "```"):
		fence = "```"
	case strings.HasPrefix(s, "~~~"):
		fence = "~~~"
	default:
		return s
	}
	rest := s[len(fence):]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	}
	rest = strings.TrimRight(rest, " \t\r\n")
	if i := strings.LastIndex(rest, fence); i >= 0 && strings.TrimSpace(rest[i:]) == fence {
		rest = rest[:i]
	}
	return strings.TrimSpace(rest)
}

func stripJSONComments(s string) string {
	var b strings.Builder
	inStr := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			b.WriteByte(c)
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			b.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '/' {
			for i+1 < len(s) && s[i+1] != '\n' {
				i++
			}
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '*' {
			i += 2
			for i < len(s) && !(s[i] == '*' && i+1 < len(s) && s[i+1] == '/') {
				i++
			}
			if i < len(s) && s[i] == '*' {
				i++
			}
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func stripTrailingCommas(s string) string {
	var b strings.Builder
	inStr := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			b.WriteByte(c)
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			b.WriteByte(c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\n' || s[j] == '\r' || s[j] == '\t') {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

func readEvents(output []byte) (n int, truncated bool) {
	if len(bytes.TrimSpace(output)) == 0 {
		return 0, false
	}
	lines := bytes.Split(output, []byte("\n"))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if json.Valid(line) {
			n++
			continue
		}
		truncated = true
	}
	return n, truncated
}
