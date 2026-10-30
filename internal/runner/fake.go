package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cardex/internal/model"
)

// Fake is a complete runner driven by a JSON script.
// The script's attempts object is keyed by attempt index.
type Fake struct{}

type scriptDoc struct {
	Attempts map[string]scriptAttempt `json:"attempts"`
}

type scriptAttempt struct {
	WriteFiles       []scriptFile `json:"write_files"`
	ChangeFuncs      []scriptFunc `json:"change_funcs"`
	Result           *string      `json:"result"`
	ExitCode         *int         `json:"exit_code"`
	TruncatedEvents  bool         `json:"truncated_events"`
	QuotaHit         *scriptQuota `json:"quota_hit"`
	AuthFailure      bool         `json:"auth_failure"`
	PermissionDenied bool         `json:"permission_denied"`
	Refusal          bool         `json:"refusal"`
	Sleep            string       `json:"sleep"`
	Silent           bool         `json:"silent"`
}

type scriptFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type scriptFunc struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Body string `json:"body"`
}

type scriptQuota struct {
	Reset string `json:"reset"`
}

func (Fake) Name() string            { return "fake" }
func (Fake) Capabilities() []string  { return []string{"fake"} }
func (Fake) NeedsPTY() bool          { return false }
func (Fake) WritablePaths() []string { return nil }

// Build returns the command line and environment that describe a fake run.
func (Fake) Build(spec LaunchSpec) ([]string, []string, error) {
	cmd := []string{"fake", "--attempt", spec.AttemptID, "--index", strconv.Itoa(spec.AttemptIndex)}
	var env []string
	if spec.ScriptPath != "" {
		env = append(env, "CARDEX_FAKE_SCRIPT="+spec.ScriptPath)
	}
	if spec.WorkDir != "" {
		env = append(env, "CARDEX_FAKE_WORKDIR="+spec.WorkDir)
	}
	return cmd, env, nil
}

// Drive does nothing. Fake runs through Run, not a PTY.
func (Fake) Drive(context.Context, io.ReadWriter) error { return nil }

// Classify maps scripted error text onto the §5 error classes.
func (Fake) Classify(exit int, output []byte) model.ErrorClass {
	text := string(output)
	switch {
	case strings.Contains(text, "quota hit"):
		return model.ErrorQuotaHit
	case strings.Contains(text, "authentication failed"):
		return model.ErrorAuthFailed
	case strings.Contains(text, "permission denied"):
		return model.ErrorPermissionDenied
	case strings.Contains(text, "refusal"):
		return model.ErrorRefusal
	default:
		return ""
	}
}

var usagePattern = regexp.MustCompile(`tokens=(\d+)`)

// ParseUsage reads a "tokens=N" marker from the output.
func (Fake) ParseUsage(output []byte) model.Usage {
	m := usagePattern.FindSubmatch(output)
	if m == nil {
		return model.Usage{}
	}
	n, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil {
		return model.Usage{}
	}
	return model.Usage{Tokens: n, Source: "fake", Confidence: 1}
}

// Run applies the script entry for spec.AttemptIndex inside spec.WorkDir.
func (f Fake) Run(ctx context.Context, spec LaunchSpec) (RunResult, error) {
	raw := spec.Script
	if len(raw) == 0 {
		if spec.ScriptPath == "" {
			return RunResult{}, fmt.Errorf("fake: no script")
		}
		var err error
		raw, err = os.ReadFile(spec.ScriptPath)
		if err != nil {
			return RunResult{}, err
		}
	}
	var doc scriptDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return RunResult{}, fmt.Errorf("fake script: %w", err)
	}
	key := strconv.Itoa(spec.AttemptIndex)
	step, ok := doc.Attempts[key]
	if !ok {
		return RunResult{}, fmt.Errorf("fake script: no attempt %s", key)
	}
	if spec.WorkDir == "" {
		return RunResult{}, fmt.Errorf("fake: empty workdir")
	}
	if err := os.MkdirAll(spec.WorkDir, 0o755); err != nil {
		return RunResult{}, err
	}
	if step.Silent {
		return RunResult{ExitCode: 0}, nil
	}
	if step.Sleep != "" {
		d, err := time.ParseDuration(step.Sleep)
		if err != nil {
			return RunResult{}, fmt.Errorf("fake sleep: %w", err)
		}
		timer := time.NewTimer(d)
		select {
		case <-ctx.Done():
			timer.Stop()
			return RunResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	for _, file := range step.WriteFiles {
		dest, err := safeJoin(spec.WorkDir, file.Path)
		if err != nil {
			return RunResult{}, err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return RunResult{}, err
		}
		if err := os.WriteFile(dest, []byte(file.Content), 0o644); err != nil {
			return RunResult{}, err
		}
	}
	for _, fn := range step.ChangeFuncs {
		dest, err := safeJoin(spec.WorkDir, fn.Path)
		if err != nil {
			return RunResult{}, err
		}
		src, err := os.ReadFile(dest)
		if err != nil {
			return RunResult{}, err
		}
		next, err := replaceFunc(src, fn.Name, fn.Body)
		if err != nil {
			return RunResult{}, err
		}
		if err := os.WriteFile(dest, next, 0o644); err != nil {
			return RunResult{}, err
		}
	}
	if step.Result != nil {
		dest := filepath.Join(spec.WorkDir, ".cardex-result.md")
		if err := os.WriteFile(dest, []byte(*step.Result), 0o644); err != nil {
			return RunResult{}, err
		}
	}
	var out bytes.Buffer
	if step.TruncatedEvents {
		out.WriteString(truncatedEventStream())
	}
	if step.QuotaHit != nil {
		fmt.Fprintf(&out, "error: quota hit, reset at %s\n", step.QuotaHit.Reset)
	}
	if step.AuthFailure {
		out.WriteString("error: authentication failed\n")
	}
	if step.PermissionDenied {
		out.WriteString("error: permission denied: operation rejected\n")
	}
	if step.Refusal {
		out.WriteString("error: refusal: the model refused the request\n")
	}
	code := 0
	if step.ExitCode != nil {
		code = *step.ExitCode
	}
	return RunResult{ExitCode: code, Output: out.Bytes()}, nil
}

func truncatedEventStream() string {
	full := "{\"type\":\"attempt_started\",\"ok\":true}\n{\"type\":\"tool\",\"name\":\"edit\",\"partial\":true}\n"
	// Drop the closing of the last object so the stream cannot be parsed to the end.
	return full[:len(full)-8]
}

func safeJoin(root, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("fake: path %q must be relative", rel)
	}
	full := filepath.Join(root, rel)
	relCheck, err := filepath.Rel(root, full)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("fake: path %q escapes workdir", rel)
	}
	return full, nil
}

func replaceFunc(src []byte, name, body string) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "src.go", src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var target *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if funcKey(fn) == name || fn.Name.Name == name {
			target = fn
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("fake: function %s not found", name)
	}
	open := fset.Position(target.Body.Lbrace).Offset
	closePos := fset.Position(target.Body.Rbrace).Offset
	var buf bytes.Buffer
	buf.Write(src[:open+1])
	buf.WriteByte('\n')
	buf.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		buf.WriteByte('\n')
	}
	buf.Write(src[closePos:])
	next := buf.Bytes()
	if _, err := parser.ParseFile(token.NewFileSet(), "src.go", next, 0); err != nil {
		return nil, fmt.Errorf("fake: replaced %s does not parse: %w", name, err)
	}
	return next, nil
}

func funcKey(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return recvName(fn.Recv.List[0].Type) + "." + fn.Name.Name
}

func recvName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return recvName(e.X)
	default:
		return ""
	}
}
