package harvest

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// TestFailure is one failed test. Context is set only for a new failure, and is at most 10 lines.
type TestFailure struct {
	Name    string `json:"name"`
	Context string `json:"context,omitempty"`
}

// TestBaseline is the comparison of one go test command against the same command at the card base.
// Status is "compared" or "unparsed". Unparsed carries an exit code and no failure lists.
type TestBaseline struct {
	Status string        `json:"status"`
	Exit   *int          `json:"exit,omitempty"`
	New    []TestFailure `json:"new_failures,omitempty"`
	Gone   []string      `json:"gone_failures,omitempty"`
	Still  []string      `json:"still_failures,omitempty"`
}

var (
	failLineRE = regexp.MustCompile(`^\s*--- FAIL: (\S+)`)
	signalRE   = regexp.MustCompile(`^\s*--- (FAIL|PASS|SKIP): |^(ok|FAIL|\?)\s+`)
)

type parsedTests struct {
	ok      bool
	names   []string
	context map[string]string
}

func isGoTest(command string) bool {
	fields := strings.Fields(command)
	return len(fields) >= 2 && fields[0] == "go" && fields[1] == "test"
}

func parseGoTestOutput(out string, exit int) parsedTests {
	lines := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	if parsed, jsonOutput := parseGoTestJSON(lines, exit); jsonOutput {
		return parsed
	}
	parsed := parsedTests{context: map[string]string{}}
	var failures []TestFailure
	flush := func(pkg string) {
		for _, failure := range failures {
			parsed.addFailure(testName(pkg, failure.Name), failure.Context)
		}
		failures = failures[:0]
	}
	for i, line := range lines {
		if line == "PASS" || line == "FAIL" || signalRE.MatchString(line) {
			parsed.ok = true
		}
		status, pkg, found := strings.Cut(line, "\t")
		if found && (status == "ok" || status == "FAIL") {
			if status == "FAIL" && (len(failures) == 0 || strings.HasSuffix(line, "failed]")) {
				return parsedTests{}
			}
			pkg, _, _ = strings.Cut(pkg, "\t")
			flush(pkg)
		}
		if match := failLineRE.FindStringSubmatch(line); match != nil {
			failures = append(failures, TestFailure{Name: match[1], Context: failureContext(lines[i:], 10)})
		}
	}
	flush("")
	parsed.ok = parsed.ok && exit >= 0 && (exit == 0 || len(parsed.names) > 0)
	return parsed
}

func parseGoTestJSON(lines []string, exit int) (parsedTests, bool) {
	parsed := parsedTests{context: map[string]string{}}
	contexts := map[string][]string{}
	failedPackages := map[string]bool{}
	namedFailures := map[string]bool{}
	recognized, invalid := false, false
	for _, line := range lines {
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var event struct {
			Action  string
			Package string
			Test    string
			Output  string
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			invalid = true
			continue
		}
		switch event.Action {
		case "build-fail":
			recognized, invalid = true, true
		case "pass", "skip", "fail":
			recognized = true
			if event.Action == "fail" {
				if event.Test == "" {
					failedPackages[event.Package] = true
				} else {
					name := testName(event.Package, event.Test)
					namedFailures[event.Package] = true
					parsed.addFailure(name, strings.Join(contexts[name], "\n"))
				}
			}
			if event.Test != "" {
				delete(contexts, testName(event.Package, event.Test))
			}
		case "output":
			if event.Test == "" {
				continue
			}
			name := testName(event.Package, event.Test)
			for _, text := range strings.Split(strings.TrimSuffix(event.Output, "\n"), "\n") {
				if len(contexts[name]) >= 10 {
					break
				}
				contexts[name] = append(contexts[name], text)
			}
		}
	}
	for pkg := range failedPackages {
		if !namedFailures[pkg] {
			invalid = true
		}
	}
	parsed.ok = recognized && !invalid && exit >= 0 && (exit == 0 || len(parsed.names) > 0)
	return parsed, recognized
}

func testName(pkg, name string) string {
	if pkg == "" {
		return name
	}
	return pkg + "/" + name
}

func (p *parsedTests) addFailure(name, context string) {
	if _, seen := p.context[name]; !seen {
		p.names = append(p.names, name)
		p.context[name] = context
	}
}

func failureContext(lines []string, limit int) string {
	if limit <= 0 || len(lines) == 0 {
		return ""
	}
	var kept []string
	for i, line := range lines {
		if i > 0 && (line == "PASS" || line == "FAIL" || signalRE.MatchString(line)) {
			break
		}
		kept = append(kept, line)
		if len(kept) == limit {
			break
		}
	}
	return strings.Join(kept, "\n")
}

func compareParsed(current, base parsedTests, baseExit int, baseRan bool) *TestBaseline {
	out := &TestBaseline{Status: "unparsed"}
	if !current.ok || !baseRan || !base.ok {
		if baseRan {
			exit := baseExit
			out.Exit = &exit
		}
		return out
	}
	exit := baseExit
	out.Status = "compared"
	out.Exit = &exit
	cur := map[string]bool{}
	prev := map[string]bool{}
	for _, name := range current.names {
		cur[name] = true
	}
	for _, name := range base.names {
		prev[name] = true
	}
	for _, name := range current.names {
		if prev[name] {
			out.Still = append(out.Still, name)
			continue
		}
		out.New = append(out.New, TestFailure{Name: name, Context: current.context[name]})
	}
	for _, name := range base.names {
		if !cur[name] {
			out.Gone = append(out.Gone, name)
		}
	}
	sort.Slice(out.New, func(i, j int) bool { return out.New[i].Name < out.New[j].Name })
	sort.Strings(out.Gone)
	sort.Strings(out.Still)
	return out
}
