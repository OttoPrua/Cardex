package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	WriteFiles       []scriptFile      `json:"write_files"`
	ChangeFuncs      []scriptFunc      `json:"change_funcs"`
	Result           *string           `json:"result"`
	ExitCode         *int              `json:"exit_code"`
	TruncatedEvents  bool              `json:"truncated_events"`
	QuotaHit         *scriptQuota      `json:"quota_hit"`
	AuthFailure      bool              `json:"auth_failure"`
	PermissionDenied bool              `json:"permission_denied"`
	Refusal          bool              `json:"refusal"`
	Sleep            string            `json:"sleep"`
	Silent           bool              `json:"silent"`
	Output           string            `json:"output"`
	Stderr           string            `json:"stderr"`
	OutsideWrite     string            `json:"outside_write"`
	GrandchildMarker string            `json:"grandchild_marker"`
	GrandchildAfter  string            `json:"grandchild_after"`
	GrandchildDetach bool              `json:"grandchild_detach"`
	TailAfter        string            `json:"tail_after"`
	Tail             string            `json:"tail"`
	Hold             string            `json:"hold"`
	Env              map[string]string `json:"env"`
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
	if spec.PromptPath != "" {
		env = append(env, "CARDEX_FAKE_PROMPT="+spec.PromptPath)
	}
	raw := spec.Script
	if len(raw) == 0 && spec.ScriptPath != "" {
		raw, _ = os.ReadFile(spec.ScriptPath)
	}
	env = append(env, scriptEnv(raw, spec.AttemptIndex)...)
	return cmd, env, nil
}

func scriptEnv(raw []byte, index int) []string {
	var doc scriptDoc
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	step, ok := doc.Attempts[strconv.Itoa(index)]
	if !ok || len(step.Env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(step.Env))
	for k := range step.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, k+"="+step.Env[k])
	}
	return env
}

var fakeDriveCount atomic.Uint64

var (
	fakeDriveMu   sync.Mutex
	fakeDriveLast []byte
)

// FakeDriveCount is how many times Fake.Drive has been entered in this process.
func FakeDriveCount() uint64 { return fakeDriveCount.Load() }

// FakeDriveCapture is the bytes the last Fake.Drive read from its stream.
func FakeDriveCapture() []byte {
	fakeDriveMu.Lock()
	defer fakeDriveMu.Unlock()
	return append([]byte(nil), fakeDriveLast...)
}

func rememberDrive(b []byte) {
	fakeDriveMu.Lock()
	fakeDriveLast = append([]byte(nil), b...)
	fakeDriveMu.Unlock()
}

// Drive reads the executor stream until EOF. A line beginning with
// "drive_error:" is returned to the host. Prompt, completion, and
// budget_limited lines stay in FakeDriveCapture.
func (Fake) Drive(ctx context.Context, rw io.ReadWriter) error {
	fakeDriveCount.Add(1)
	var buf bytes.Buffer
	tmp := make([]byte, 4096)
	var driveErr error
	for {
		if err := ctx.Err(); err != nil {
			rememberDrive(buf.Bytes())
			if driveErr != nil {
				return driveErr
			}
			return err
		}
		n, err := rw.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
			if driveErr == nil {
				if msg, ok := driveErrorLine(buf.Bytes()); ok {
					driveErr = errors.New(msg)
				}
			}
			if driveTerminalReached(buf.Bytes()) {
				rememberDrive(buf.Bytes())
				return driveErr
			}
		}
		if err != nil {
			rememberDrive(buf.Bytes())
			if driveErr != nil {
				return driveErr
			}
			if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
				return nil
			}
			return err
		}
	}
}

func driveErrorLine(b []byte) (string, bool) {
	for _, line := range bytes.Split(b, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("drive_error:")) {
			return string(line), true
		}
	}
	return "", false
}

// driveTerminalReached reports a recognized completion, budget_limited, or
// error terminal. Drive returns at that point even if the executor keeps
// writing.
func driveTerminalReached(b []byte) bool {
	for _, line := range bytes.Split(b, []byte("\n")) {
		line = bytes.TrimSpace(line)
		switch string(line) {
		case "completion", "budget_limited", "drive_return":
			return true
		}
		if bytes.HasPrefix(line, []byte("drive_error:")) || bytes.HasPrefix(line, []byte("drive_return:")) {
			return true
		}
	}
	return false
}

// Classify maps structured errors and explicit diagnostics onto §5 classes.
func (Fake) Classify(exit int, output []byte) model.ErrorClass {
	return ClassifyAttempt(exit, output)
}

// ParseUsage honors a tokens=N marker. Real usage documents are not relabeled
// as fake when that marker is absent.
func (Fake) ParseUsage(output []byte) model.Usage {
	if u, ok := parseFakeTokens(output); ok {
		return u
	}
	return ParseOutputUsage(output)
}

// NeedsPTYFor reports the script's needs_pty flag. NeedsPTY stays false.
func (Fake) NeedsPTYFor(spec LaunchSpec) bool {
	raw := spec.Script
	if len(raw) == 0 && spec.ScriptPath != "" {
		raw, _ = os.ReadFile(spec.ScriptPath)
	}
	var doc struct {
		NeedsPTY bool `json:"needs_pty"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return false
	}
	return doc.NeedsPTY
}

// Run applies the script entry for spec.AttemptIndex inside spec.WorkDir.
func (f Fake) Run(ctx context.Context, spec LaunchSpec) (RunResult, error) {
	return f.run(ctx, spec, false)
}

func (f Fake) run(ctx context.Context, spec LaunchSpec, child bool) (RunResult, error) {
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
	if step.OutsideWrite != "" && !child {
		if _, err := safeJoin(spec.WorkDir, step.OutsideWrite); err != nil {
			return RunResult{}, err
		}
		return RunResult{}, fmt.Errorf("fake: path %q escapes workdir", step.OutsideWrite)
	}
	if step.Silent {
		return RunResult{ExitCode: 0}, nil
	}
	var prelude bytes.Buffer
	if child {
		if err := childPrelude(&prelude, spec, step); err != nil {
			return RunResult{}, err
		}
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
		writeDiagnostic(&out, fmt.Sprintf("quota hit, reset at %s", step.QuotaHit.Reset))
	}
	if step.AuthFailure {
		writeDiagnostic(&out, "authentication failed")
	}
	if step.PermissionDenied {
		writeDiagnostic(&out, "permission denied: operation rejected")
	}
	if step.Refusal {
		writeDiagnostic(&out, "refusal: the model refused the request")
	}
	if step.Output != "" {
		out.WriteString(step.Output)
	}
	if step.Stderr != "" {
		if child {
			if _, err := os.Stderr.WriteString(step.Stderr); err != nil {
				return RunResult{}, err
			}
		} else {
			out.WriteString(step.Stderr)
		}
	}
	if prelude.Len() > 0 {
		var merged bytes.Buffer
		merged.Write(prelude.Bytes())
		merged.Write(out.Bytes())
		out = merged
	}
	code := 0
	if step.ExitCode != nil {
		code = *step.ExitCode
	}
	result := RunResult{ExitCode: code, Output: out.Bytes()}
	if step.TailAfter != "" {
		d, err := time.ParseDuration(step.TailAfter)
		if err != nil {
			return RunResult{}, fmt.Errorf("fake tail_after: %w", err)
		}
		result.TailAfter = d
		result.Tail = []byte(step.Tail)
	} else if step.Tail != "" {
		result.Tail = []byte(step.Tail)
	}
	if step.Hold != "" {
		d, err := time.ParseDuration(step.Hold)
		if err != nil {
			return RunResult{}, fmt.Errorf("fake hold: %w", err)
		}
		result.Hold = d
	}
	return result, nil
}

func writeDiagnostic(out *bytes.Buffer, message string) {
	fmt.Fprintf(out, "error: %s\n", message)
	encoded, err := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]string{"message": message},
	})
	if err != nil {
		return
	}
	out.Write(encoded)
	out.WriteByte('\n')
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
