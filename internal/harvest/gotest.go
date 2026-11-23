package harvest

import (
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
	failLineRE = regexp.MustCompile(`^--- FAIL: (\S+)`)
	signalRE   = regexp.MustCompile(`^(--- (FAIL|PASS|SKIP): |ok |ok\t|FAIL\t|\?   )`)
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

func parseGoTestOutput(out string) parsedTests {
	out = strings.ReplaceAll(out, "\r\n", "\n")
	lines := strings.Split(out, "\n")
	var parsed parsedTests
	parsed.context = map[string]string{}
	for i, line := range lines {
		if line == "PASS" || line == "FAIL" || signalRE.MatchString(line) {
			parsed.ok = true
		}
		match := failLineRE.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		parsed.ok = true
		name := match[1]
		if _, seen := parsed.context[name]; seen {
			continue
		}
		parsed.names = append(parsed.names, name)
		parsed.context[name] = failureContext(lines[i:], 10)
	}
	return parsed
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

func compareParsed(current parsedTests, currentExit int, base parsedTests, baseExit int, baseRan bool) *TestBaseline {
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
	_ = currentExit
	return out
}
