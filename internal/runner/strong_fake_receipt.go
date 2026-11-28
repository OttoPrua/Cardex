package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/strongcustody"
)

const strongFakeBootstrap = "--cardex-strong-fake-v1"

const strongFakeBundleEnv = "CARDEX_STRONG_FAKE_BUNDLE_SHA"
const strongFakeBundlePrefix = "strong_fake_input_sha="
const strongFakeChallengeEnv = "CARDEX_STRONG_FAKE_CHALLENGE"
const strongFakeScriptSHAEnv = "CARDEX_STRONG_FAKE_SCRIPT_SHA"
const strongFakeScriptMax = 96 << 10

type StrongFakeReceipt struct{ data StrongFakeRecord }

type StrongFakeRecord struct {
	Attempt, WorkDir, BundleSHA, Challenge string
	Exit                                   int
	Usage                                  model.Usage
}

func (r *StrongFakeReceipt) Record() StrongFakeRecord {
	if r == nil {
		return StrongFakeRecord{}
	}
	return r.data
}

// RunStrongFakeChild owns one local fixture initiation. The existing Fake
// child hashes the actual opened input, and its stdout is the only accepted
// usage source. No real adapter can reach this code path.
func RunStrongFakeChild(ctx context.Context, spec LaunchSpec, bundleSHA, challenge string, authorize, started func() error) (*StrongFakeReceipt, []byte, error) {
	if !model.StrongDigestKey(bundleSHA) || !model.StrongDigestKey(challenge) || !model.StrongKey(spec.AttemptID) ||
		spec.AttemptIndex != 0 || len(spec.Script) != 0 || len(spec.strongFixtureInput) != 0 || spec.SessionID != "" ||
		spec.ConfigDir != "" || len(spec.DropEnv) != 0 || spec.RelayURL != "" || spec.RelayKey != "" ||
		!strongFakeSafePath(spec.PromptPath) || !strongFakeSafePath(spec.WorkDir) || !strongFakeSafePath(spec.ScriptPath) ||
		authorize == nil || started == nil {
		return nil, nil, model.ErrStrongArtifact
	}
	exe, err := os.Executable()
	if err != nil || !strongFakeSafePath(exe) {
		return nil, nil, model.ErrStrongArtifact
	}
	script, err := readStrongFakeScript(spec.ScriptPath)
	if err != nil {
		return nil, nil, model.ErrStrongArtifact
	}
	scriptSHA := model.StrongArtifactDigest(script)
	child, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Never use the generic Fake builder: its script-derived env and extra
	// disk reads do not belong to the bounded strong fixture contract.
	cmd := exec.CommandContext(child, exe, strongFakeBootstrap, "--attempt", spec.AttemptID, "--index", "0")
	cmd.Env = []string{"CARDEX_FAKE_CHILD=1", "CARDEX_FAKE_WORKDIR=" + spec.WorkDir, "CARDEX_FAKE_PROMPT=" + spec.PromptPath,
		strongFakeBundleEnv + "=" + bundleSHA, strongFakeChallengeEnv + "=" + challenge, strongFakeScriptSHAEnv + "=" + scriptSHA}
	cmd.Stdin = bytes.NewReader(script)
	var buf bytes.Buffer
	cmd.Stdout = &limitedStrongFakeOutput{buf: &buf, max: 256 << 10}
	if err := authorize(); err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	if err := started(); err != nil {
		cancel()
		_ = cmd.Wait()
		return nil, nil, err
	}
	err = cmd.Wait()
	exit := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || child.Err() != nil {
			return nil, nil, err
		}
		exit = exitErr.ExitCode()
	}
	if !strictStrongTransport(buf.Bytes(), strongFakeProof(bundleSHA, challenge, spec.AttemptID, scriptSHA)) {
		return nil, nil, model.ErrStrongArtifact
	}
	out := bytes.Clone(buf.Bytes())
	return &StrongFakeReceipt{data: StrongFakeRecord{Attempt: spec.AttemptID, WorkDir: spec.WorkDir, BundleSHA: bundleSHA, Challenge: challenge, Exit: exit, Usage: strongFakeExactUsage(out)}}, out, nil
}

type limitedStrongFakeOutput struct {
	buf *bytes.Buffer
	max int
}

func (w *limitedStrongFakeOutput) Write(p []byte) (int, error) {
	if len(p) > w.max-w.buf.Len() {
		return 0, io.ErrShortWrite
	}
	return w.buf.Write(p)
}

func strictStrongTransport(out []byte, expected string) bool {
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, strongFakeBundlePrefix) {
			continue
		}
		if strings.TrimPrefix(line, strongFakeBundlePrefix) != expected {
			return false
		}
		n++
	}
	return n == 1
}

func strongFakeExactUsage(out []byte) model.Usage {
	var n int64
	seen := false
	for _, line := range strings.Split(string(out), "\n") {
		normalized := strings.TrimSpace(line)
		if !strings.Contains(strings.ToLower(normalized), "tokens") {
			continue
		}
		if seen || line != normalized || !strings.HasPrefix(line, "tokens=") {
			return model.Usage{}
		}
		raw := strings.TrimPrefix(line, "tokens=")
		got, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || got <= 0 || got > model.StrongTokenCeiling || strconv.FormatInt(got, 10) != raw {
			return model.Usage{}
		}
		seen, n = true, got
	}
	if !seen {
		return model.Usage{}
	}
	return model.Usage{Tokens: n, Source: "strong_fake_child", Confidence: 1}
}

func strongFakeProof(bundle, challenge, attempt, scriptSHA string) string {
	return model.StrongArtifactDigest([]byte(bundle + ":" + challenge + ":" + attempt + ":" + scriptSHA))
}

func strongFakeSafePath(path string) bool {
	return len(path) > 0 && len(path) <= 4096 && utf8.ValidString(path) && filepath.IsAbs(path) && filepath.Clean(path) == path &&
		!strings.ContainsFunc(path, unicode.IsControl)
}

func readStrongFakeScript(path string) ([]byte, error) {
	raw, err := strongcustody.ReadFakeDelivery(path, "script.json", strongFakeScriptMax)
	if err != nil {
		return nil, model.ErrStrongArtifact
	}
	if err := validateStrongFakeScript(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func validateStrongFakeScript(raw []byte) error {
	if len(raw) == 0 || len(raw) > strongFakeScriptMax {
		return model.ErrStrongArtifact
	}

	var doc struct {
		Attempts map[string]struct {
			Result              *string                          `json:"result"`
			Output              string                           `json:"output"`
			PromptEvidenceFacts bool                             `json:"prompt_evidence_facts"`
			WriteFiles          []struct{ Path, Content string } `json:"write_files"`
		} `json:"attempts"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&doc) != nil || len(doc.Attempts) != 1 {
		return model.ErrStrongArtifact
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return model.ErrStrongArtifact
	}
	step, ok := doc.Attempts["0"]
	if !ok || step.Result == nil || len(step.WriteFiles) > 1 {
		return model.ErrStrongArtifact
	}
	for _, file := range step.WriteFiles {
		if file.Path != ".cardex-strong-references.json" || len(file.Content) > 1024 {
			return model.ErrStrongArtifact
		}
	}
	return nil
}
