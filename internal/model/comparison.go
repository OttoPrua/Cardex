package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

const RequireStrictComparison = "strict_model_comparison"
const ModelSourceProvider = "provider_response"
const ModelSourceUnknown = "unknown"
const UnknownProviderModel = "unknown"
const ComparisonMaxQuestion = 16 << 10
const ComparisonMaxAnswer = 32 << 10

const comparisonInstruction = "Answer only the approved comparison question below. Do not use tools.\n\n"

// StrictComparison is an explicit card capability, not inferred from a pin
// or from a provider's output.
func (c Card) StrictComparison() bool {
	return slices.Contains(c.Requires, RequireStrictComparison)
}

func (c Card) ValidStrictComparison() bool {
	return c.StrictComparison() && c.Kind == KindResearch && c.Node == "" &&
		c.Channel != "" && c.Model != "" && c.Sandbox != "off" && len(c.Requires) == 1 &&
		strings.TrimSpace(c.Body) != "" && len(c.Body) <= ComparisonMaxQuestion &&
		!strings.ContainsRune(c.Body, '\x00')
}

// ComparisonInput is intentionally independent of the project, worktree,
// stage, acceptance commands, tool context, and prompt plugins.
func (c Card) ComparisonInput() (prompt string, digest string) {
	if !c.ValidStrictComparison() {
		return "", ""
	}
	prompt = comparisonInstruction + c.Body
	raw, _ := json.Marshal([]string{"cardex-comparison-input-v1", c.Channel, c.Model, prompt})
	hash := sha256.Sum256(raw)
	return prompt, hex.EncodeToString(hash[:])
}

// ComparisonResult is separate from ordinary worktree artifacts and scores.
// Only the bounded provider answer and verified model identity are kept.
type ComparisonResult struct {
	InputDigest string                `json:"input_digest"`
	Identity    ProviderModelEvidence `json:"identity"`
	Answer      string                `json:"answer"`
}

// ProviderModelEvidence contains only the allowlisted identity fields from
// an authenticated compatible Messages response.
type ProviderModelEvidence struct {
	Requested string `json:"requested_model"`
	Returned  string `json:"returned_model"`
	Source    string `json:"source"`
	Status    string `json:"status"`
}

func (a Attempt) StrictComparison() bool {
	return a.RouteReason.Dispatch != nil && a.RouteReason.Dispatch.StrictComparison
}

func (a Attempt) VerifiedComparison(c Card) bool {
	d, e := a.RouteReason.Dispatch, a.RouteReason.ProviderModel
	_, digest := c.ComparisonInput()
	return digest != "" && a.StrictComparison() &&
		d.Channel == c.Channel && d.Model == c.Model && d.CompatibleBindingID != "" && d.ComparisonInputDigest == digest &&
		a.Route.Channel == c.Channel && a.Route.Model == c.Model && e != nil &&
		e.Status == "verified" && e.Source == ModelSourceProvider &&
		e.Requested == c.Model && e.Returned == c.Model
}
