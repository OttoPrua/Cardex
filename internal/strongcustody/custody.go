// Package strongcustody owns complete strong result capture. Other packages
// can consume a sealed result but cannot manufacture one from display bytes.
package strongcustody

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sync"

	"github.com/OttoPrua/Cardex/internal/model"
)

const strongResultName = ".cardex-result.md"
const strongReferencesName = ".cardex-strong-references.json"

// RegistrationVerifier must belong to the trusted controller. The persisted
// registration is checked again by the store before accepting the seal.
type RegistrationVerifier interface {
	CheckStrongProducer(context.Context, model.StrongProducerRegistration, *model.StrongCaptureClaim) error
	ClaimStrongProducer(context.Context, model.StrongProducerRegistration, string, string, []byte) (model.StrongCaptureClaim, error)
}

type Capture struct {
	mu        sync.Mutex
	reg       model.StrongProducerRegistration
	claim     model.StrongCaptureClaim
	input     []byte
	workspace *os.File
	verifier  RegistrationVerifier
}

// Prepare witnesses both canonical output slots empty on the preregistered
// attempt's pinned workspace. Only tested OS handle implementations are used.
func Prepare(ctx context.Context, verifier RegistrationVerifier, attempt model.Attempt, reg model.StrongProducerRegistration, taskSHA, approvedManifestSHA string, rendered []byte) (*Capture, error) {
	if verifier == nil || !sameAttempt(attempt, reg) || verifier.CheckStrongProducer(ctx, reg, nil) != nil ||
		attempt.PID != 0 || !attempt.StartedAt.IsZero() || !attempt.EndedAt.IsZero() || attempt.Exit != nil || attempt.Outcome != "" {
		return nil, model.ErrStrongArtifact
	}
	dir, err := openStrongWorkspace(reg.Worktree)
	if err != nil {
		return nil, model.ErrStrongArtifact
	}
	for _, name := range []string{strongResultName, strongReferencesName} {
		if !strongSlotAbsent(dir, name) {
			_ = dir.Close()
			return nil, model.ErrStrongArtifact
		}
	}
	launchInput := bytes.Clone(rendered)
	claim, err := verifier.ClaimStrongProducer(ctx, reg, taskSHA, approvedManifestSHA, launchInput)
	if err != nil || !claim.Valid() || claim.ManifestSHA != approvedManifestSHA || claim.BundleSHA != model.StrongArtifactDigest(launchInput) {
		_ = dir.Close()
		return nil, model.ErrStrongArtifact
	}
	return &Capture{workspace: dir, reg: reg, claim: claim, input: launchInput, verifier: verifier}, nil
}

func sameAttempt(a model.Attempt, reg model.StrongProducerRegistration) bool {
	return reg.Valid() && a.ID == reg.Attempt && a.Card == reg.Card && a.Worktree == reg.Worktree
}

// LaunchInput returns the exact pinned runner input, not the caller's
// original mutable slice. A controller stages this copy via file transport.
func (c *Capture) LaunchInput() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.input)
}

func (c *Capture) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.close()
}

func (c *Capture) close() error {
	if c.workspace == nil {
		return nil
	}
	c.input = nil
	err := c.workspace.Close()
	c.workspace = nil
	return err
}

// Result is an opaque seal, not a general-purpose body+hash struct.
type Result struct{ data Record }

// Record is a read-only copy of the sealed data. Changing a Record does not
// change the seal and the store accepts only the opaque seal itself.
type Record struct {
	Producer            model.StrongProducerRegistration
	Claim               model.StrongCaptureClaim
	Origin, Format, SHA string
	Size                int
	Body                []byte
	References          []string
	ReferenceBytes      []byte
}

func (r *Result) Record() Record {
	if r == nil {
		return Record{}
	}
	out := r.data
	out.Body = bytes.Clone(out.Body)
	out.References = slices.Clone(out.References)
	out.ReferenceBytes = bytes.Clone(out.ReferenceBytes)
	return out
}

// Seal reads both canonical files before any other command mutates the
// workspace. The preceding attempt process tree must have stopped. The
// handle is single-use and consumed even for a missing or invalid result.
func (c *Capture) Seal(ctx context.Context, attempt model.Attempt) (*Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.close()
	if c.workspace == nil || !sameAttempt(attempt, c.reg) || c.verifier.CheckStrongProducer(ctx, c.reg, &c.claim) != nil ||
		!strongWorkspaceCurrent(c.workspace, c.reg.Worktree) {
		return nil, model.ErrStrongArtifact
	}
	raw, err := readStrongAt(c.workspace, strongResultName, model.StrongResultMax)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !model.StrongResultMarkdown(raw) {
		return nil, model.ErrStrongArtifact
	}
	refBytes, err := readStrongAt(c.workspace, strongReferencesName, 1024)
	var refs []string
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil || !bytes.HasPrefix(bytes.TrimSpace(refBytes), []byte{'['}) || json.Unmarshal(refBytes, &refs) != nil:
		return nil, model.ErrStrongArtifact
	}
	if !strongWorkspaceCurrent(c.workspace, c.reg.Worktree) {
		return nil, model.ErrStrongArtifact
	}
	return &Result{data: Record{Producer: c.reg, Claim: c.claim, Origin: model.StrongResultFileOrigin, Format: model.StrongResultV1,
		SHA: model.StrongArtifactDigest(raw), Size: len(raw), Body: raw, References: refs, ReferenceBytes: refBytes}}, nil
}
