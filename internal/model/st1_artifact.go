package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

// StrongResultV1 is verbatim successful result-file Markdown, not a log,
// summary, or reconstructed transcript. Limits reject; they never truncate.
const StrongResultV1 = "cardex.result-markdown.v1"
const StrongResultMax = 2 << 20
const StrongBundleMax = 4 << 20

var evidenceOmission = regexp.MustCompile(`(?m)^omitted [0-9]+ bytes\r?$`)

var ErrStrongArtifact = errors.New("strong-thinking artifact unavailable or invalid")

// StrongArtifactRef is private scheduler metadata. Do not put it into prompts,
// runner-facing files, or public reports.
type StrongArtifactRef struct {
	Run, Stage, Attempt string
	Origin, Format, SHA string
	EnvelopeSHA         string
	Size                int
}

func StrongArtifactDigest(body []byte) string {
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}

func StrongResultBody(body []byte) bool {
	return len(body) > 0 && len(body) <= StrongResultMax && len(bytes.TrimSpace(body)) > 0 &&
		utf8.Valid(body) && !bytes.ContainsRune(body, 0) &&
		!bytes.Contains(body, []byte("\n...[truncated]...\n")) && !evidenceOmission.Match(body)
}

// StrongKey permits opaque logical keys, not paths or composite references.
func StrongKey(s string) bool {
	return len(s) > 0 && len(s) <= 128 &&
		strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") == ""
}

const StrongResultFileOrigin = "harvest.result-file.complete"

// StrongResultMarkdown is the dedicated v1 canonical result format. Unlike
// ordinary disposition, this is not a tolerant parser of control signals.
func StrongResultMarkdown(body []byte) bool {
	if !StrongResultBody(body) {
		return false
	}
	body = bytes.TrimPrefix(body, []byte("\uFEFF"))
	line, rest, found := bytes.Cut(body, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	return found && len(bytes.TrimSpace(rest)) > 0 && (string(line) == "## 结论" || string(line) == "## 复核结论")
}

type StrongRole string

const (
	StrongBlind             StrongRole = "blind"
	StrongPeer              StrongRole = "peer"
	StrongSynthesis         StrongRole = "synthesis"
	StrongRecheck           StrongRole = "recheck"
	StrongEvidence          StrongRole = "evidence"
	StrongWorkflowBlind     StrongRole = "workflow_blind"
	StrongWorkflowPeer      StrongRole = "workflow_peer"
	StrongWorkflowSynthesis StrongRole = "workflow_synthesis"
	StrongWorkflowEvidence  StrongRole = "workflow_evidence"
	StrongFraming           StrongRole = "workflow_framing"
	StrongVerification      StrongRole = "workflow_verification"
)

// StrongStage is trusted controller scope, not recipient-visible metadata.
type StrongStage struct {
	Run, Stage, Seat string
	Role             StrongRole
}

func (s StrongStage) Valid() bool {
	if !StrongKey(s.Run) || !StrongKey(s.Stage) {
		return false
	}
	switch s.Role {
	case StrongBlind, StrongPeer, StrongWorkflowBlind, StrongWorkflowPeer:
		return StrongKey(s.Seat)
	case StrongSynthesis, StrongRecheck, StrongEvidence, StrongFraming, StrongVerification, StrongWorkflowSynthesis, StrongWorkflowEvidence:
		return s.Seat == ""
	default:
		return false
	}
}

// StrongProducerRegistration binds a pending ledger attempt to the precise
// producer and trusted workspace before any output can exist. Never export it.
type StrongProducerRegistration struct {
	Source                         StrongStage
	Attempt, Card, Worktree, Token string
}

func (p StrongProducerRegistration) Valid() bool {
	return p.Source.Valid() && StrongKey(p.Attempt) && StrongKey(p.Card) &&
		p.Worktree != "" && len(p.Token) == 64 && strings.Trim(p.Token, "0123456789abcdef") == ""
}

func StrongDigestKey(s string) bool {
	return len(s) == 64 && strings.Trim(s, "0123456789abcdef") == ""
}

// StrongCaptureClaim pins the exact independently rendered runner input and
// ordered source bindings. Its token belongs to one committed capture only.
type StrongCaptureClaim struct {
	Token, ManifestSHA, BundleSHA, TaskSHA string
}

func (c StrongCaptureClaim) Valid() bool {
	return StrongDigestKey(c.Token) && StrongDigestKey(c.ManifestSHA) &&
		StrongDigestKey(c.BundleSHA) && StrongDigestKey(c.TaskSHA)
}

// StrongEmptyManifestSHA pins the canonical ordered zero-source manifest.
func StrongEmptyManifestSHA() string { return StrongArtifactDigest([]byte("[]")) }
