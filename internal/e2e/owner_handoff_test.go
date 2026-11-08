package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ownerHandoffHeading = "## Handing a decision to 使用者（owner）"

// managementSessionOwnerSites are the write-domain files that tell a Hermes or
// Codex management session which decisions to hand to the user. docs/v2/guide.md
// speaks to the user and is not one of these sites.
func managementSessionOwnerSites(t *testing.T, root string) []string {
	t.Helper()
	dir := filepath.Join(root, "skills", "cardex-dispatch")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var sites []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), ownerHandoffHeading) {
			sites = append(sites, path)
		}
	}
	shipped := filepath.Join(root, "skills", "cardex-dispatch", "SKILL.md")
	found := false
	for _, site := range sites {
		if site == shipped {
			found = true
		}
	}
	if !found {
		t.Fatalf("shipped skill %s is not a user handoff site; found %v", shipped, sites)
	}
	return sites
}

func ownerHandoffSection(body string) string {
	i := strings.Index(body, ownerHandoffHeading)
	if i < 0 {
		return ""
	}
	rest := body[i+len(ownerHandoffHeading):]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// ownerRequiredClause is the list after "required for" and before the period.
// Automatic freezes stated after that period are not user assignments.
func ownerRequiredClause(handoff string) string {
	const marker = "required for "
	i := strings.Index(handoff, marker)
	if i < 0 {
		return ""
	}
	rest := handoff[i+len(marker):]
	if j := strings.Index(rest, "."); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func e2eModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func TestManagementSessionOwnerHandoff(t *testing.T) {
	root := e2eModuleRoot(t)
	sites := managementSessionOwnerSites(t, root)
	cases := []struct {
		name   string
		phrase string
		// where is "clause" (the required list), "handoff", or "file".
		where string
		// forbid reports the phrase must be absent from where.
		forbid bool
	}{
		{name: "direction freeze", phrase: "direction freeze", where: "clause"},
		{name: "budget exhaustion", phrase: "budget exhaustion", where: "clause"},
		{name: "breaker", phrase: "a breaker", where: "clause"},
		{name: "objective-done question", phrase: "objective-done question", where: "clause"},
		{name: "safe-mode module freeze", phrase: "safe-mode module freeze", where: "clause"},
		{name: "frozen-node reopen", phrase: "reopen of an already frozen node", where: "clause"},
		{name: "positioning proposal approval", phrase: "Approving a positioning proposal uses `positioning approve <id>`", where: "file"},
		{name: "do not pick those options", phrase: "Do not pick those options in this session", where: "handoff"},
		{name: "fast-mode module freeze stays off the required list", phrase: "fast-mode", where: "clause", forbid: true},
		{name: "detail-layer freeze stays off the required list", phrase: "detail-layer", where: "clause", forbid: true},
	}
	for _, site := range sites {
		bodyBytes, err := os.ReadFile(site)
		if err != nil {
			t.Fatal(err)
		}
		body := string(bodyBytes)
		handoff := ownerHandoffSection(body)
		clause := ownerRequiredClause(handoff)
		rel, err := filepath.Rel(root, site)
		if err != nil {
			rel = site
		}
		for _, tc := range cases {
			t.Run(rel+"/"+tc.name, func(t *testing.T) {
				var haystack string
				switch tc.where {
				case "clause":
					haystack = clause
				case "handoff":
					haystack = handoff
				case "file":
					haystack = body
				default:
					t.Fatalf("bad where %q", tc.where)
				}
				if haystack == "" {
					t.Fatalf("%s has no %s text", rel, tc.where)
				}
				got := strings.Contains(haystack, tc.phrase)
				if tc.forbid && got {
					t.Fatalf("%s %s contains %q\n%s", rel, tc.where, tc.phrase, haystack)
				}
				if !tc.forbid && !got {
					t.Fatalf("%s %s omits %q\n%s", rel, tc.where, tc.phrase, haystack)
				}
			})
		}
		t.Run(rel+"/does not submit 同意", func(t *testing.T) {
			for _, phrase := range []string{"submit 同意", "提交同意", "pick 同意"} {
				if strings.Contains(handoff, phrase) {
					t.Fatalf("%s tells the session to %s\n%s", rel, phrase, handoff)
				}
			}
		})
	}
}
