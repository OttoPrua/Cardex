package harvest

import (
	"regexp"
	"strconv"
	"strings"
)

// ReviewIssue is one P0, P1, or P2 finding: one sentence plus a file and line.
type ReviewIssue struct {
	Text string `json:"text"`
	File string `json:"file"`
	Line int    `json:"line"`
}

// ReviewSummary is the structured review opinion. The original text stays in result.md.
type ReviewSummary struct {
	Verdict string        `json:"verdict"`
	P0      []ReviewIssue `json:"p0"`
	P1      []ReviewIssue `json:"p1"`
	P2      []ReviewIssue `json:"p2"`
	Tests   []string      `json:"tests"`
	Source  string        `json:"source"`
}

var (
	issueRE = regexp.MustCompile(`(?i)\bP([012])\b`)
	locRE   = regexp.MustCompile(`([A-Za-z0-9_./\\-]+\.[A-Za-z0-9]+):(\d+)`)
	testRE  = regexp.MustCompile(`(?i)^补测试\s*[:：]\s*(.+)$`)
)

func parseReviewSummary(raw string) ReviewSummary {
	out := ReviewSummary{P0: []ReviewIssue{}, P1: []ReviewIssue{}, P2: []ReviewIssue{}, Tests: []string{}}
	sections := splitSections(raw)
	var reviewBody string
	for _, section := range sections {
		if section.name == "复核结论" {
			reviewBody = section.body
		}
		if strings.Contains(section.name, "测试") || section.name == "tests" {
			out.Tests = append(out.Tests, testLines(section.body)...)
		}
	}
	verdictBody := reviewBody
	if verdictBody == "" {
		verdictBody = raw
	}
	if verdict, ok := reviewVerdict(verdictBody); ok {
		out.Verdict = verdict
	}
	if reviewBody == "" {
		reviewBody = raw
	}
	for _, line := range strings.Split(reviewBody, "\n") {
		issue, ok := issueFromLine(line)
		if !ok {
			if item, ok := inlineTest(line); ok {
				out.Tests = append(out.Tests, item)
			}
			continue
		}
		switch issue.sev {
		case "0":
			out.P0 = append(out.P0, issue.item)
		case "1":
			out.P1 = append(out.P1, issue.item)
		case "2":
			out.P2 = append(out.P2, issue.item)
		}
	}
	out.Tests = uniqueStrings(out.Tests)
	return out
}

type foundIssue struct {
	sev  string
	item ReviewIssue
}

func issueFromLine(line string) (foundIssue, bool) {
	match := issueRE.FindStringSubmatchIndex(line)
	if match == nil {
		return foundIssue{}, false
	}
	sev := line[match[2]:match[3]]
	rest := strings.TrimSpace(line[match[1]:])
	rest = strings.TrimLeft(rest, ":：-]) ")
	item := ReviewIssue{Text: strings.TrimSpace(rest)}
	if loc := locRE.FindStringSubmatchIndex(rest); loc != nil {
		item.File = rest[loc[2]:loc[3]]
		item.Line, _ = strconv.Atoi(rest[loc[4]:loc[5]])
		text := strings.TrimSpace(rest[:loc[0]] + " " + rest[loc[1]:])
		text = strings.Trim(text, " :：-")
		item.Text = strings.Join(strings.Fields(text), " ")
	}
	if item.Text == "" {
		item.Text = strings.TrimSpace(rest)
	}
	return foundIssue{sev: sev, item: item}, item.Text != "" || item.File != ""
}

func testLines(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "-* \t")
		if line == "" || issueRE.MatchString(line) {
			continue
		}
		if item, ok := inlineTest(line); ok {
			out = append(out, item)
			continue
		}
		if strings.Contains(line, " ") && !strings.HasPrefix(line, "Test") {
			continue
		}
		out = append(out, line)
	}
	return out
}

func inlineTest(line string) (string, bool) {
	line = strings.TrimSpace(strings.TrimLeft(line, "-* \t"))
	match := testRE.FindStringSubmatch(line)
	if match == nil {
		return "", false
	}
	return strings.TrimSpace(match[1]), match[1] != ""
}

func uniqueStrings(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}
