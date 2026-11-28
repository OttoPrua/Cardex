package board

import (
	"context"
	"encoding/json"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

// StrongPublicReport is a positive allowlist, not a private report with fields
// erased. No caller-provided label, identifier, string, or source hash goes in.
type StrongPublicReport struct {
	Format                   string `json:"format"`
	TaskCategory             string `json:"task_category"`
	TaskSummary              string `json:"task_summary"`
	FlowVersion              int    `json:"flow_version"`
	FlowTemplate             string `json:"flow_template"`
	Result                   string `json:"result"`
	Attempts                 int    `json:"attempts"`
	KnownSyntheticDispatches int    `json:"known_synthetic_dispatches"`
	KnownSyntheticTokens     *int64 `json:"known_synthetic_tokens"`
	UnknownUsageAttempts     int    `json:"unknown_usage_attempts"`
	ModelCalls               *int   `json:"model_calls"`
	ToolCalls                *int   `json:"tool_calls"`
	ProviderMeasuredTokens   *int64 `json:"provider_measured_tokens"`
	ScoringBasis             string `json:"scoring_basis"`
	Limitations              string `json:"limitations"`
}

type StrongExport struct {
	Report       StrongPublicReport `json:"report"`
	ReportSHA256 string             `json:"report_sha256"`
}

// ReadStrongReport shares O1's pinned, no-follow checkpoint projection.
// There is deliberately no HTTP endpoint or automatic publishing behavior.
func ReadStrongReport(ctx context.Context, path string, key store.StrongRunKey) (store.StrongRunReport, error) {
	return store.ReadPinnedStrongRunReport(ctx, path, key)
}

func RedactedStrongReport(in store.StrongRunReport) (StrongExport, error) {
	out := StrongPublicReport{Format: "cardex.synthetic-research-redacted.v1", TaskCategory: "synthetic_research",
		TaskSummary:  "A synthetic research task with private task details omitted.",
		ScoringBasis: "Complete sealed evidence and structured workflow checks; no factual score inferred.",
		Limitations:  "Offline synthetic verification only. Missing telemetry is unknown; no provider, isolation, or private-evidence reproduction claim."}
	if in.Version != model.StrongPlanVersion {
		return StrongExport{}, model.ErrStrongPlan
	}
	out.FlowVersion = model.StrongPlanVersion
	switch in.Template {
	case model.StrongTemplateLight:
		out.FlowTemplate = "light"
	case model.StrongTemplateStrong:
		out.FlowTemplate = "strong"
	default:
		return StrongExport{}, model.ErrStrongPlan
	}
	reviewOK, rewriteOK := false, false
	var majorDissent *bool
	terminalVerified := len(in.Stages) > 0
	for _, s := range in.Stages {
		if s.Attempts < 0 || s.Attempts > 1 {
			return StrongExport{}, model.ErrStrongPlan
		}
		out.Attempts += s.Attempts
		if s.Attempts == 1 {
			if s.ModelDispatches != nil && *s.ModelDispatches == 1 {
				out.KnownSyntheticDispatches++
			}
			if u := s.SyntheticMeasured; u != nil && u.Confidence == 1 && u.Tokens > 0 && u.Tokens <= model.StrongTokenCeiling && u.Source == "strong_fake_child" && s.ModelDispatches != nil && *s.ModelDispatches == 1 {
				if out.KnownSyntheticTokens == nil {
					out.KnownSyntheticTokens = new(int64)
				}
				*out.KnownSyntheticTokens += u.Tokens
			} else {
				out.UnknownUsageAttempts++
			}
		}
		if !(s.State == "skipped" && s.VerifiedConditionalSkip) && (s.State != "done" || s.ArtifactStatus != "verified_complete") {
			terminalVerified = false
		}
		if s.ArtifactStatus == "verified_complete" && s.Review != nil {
			if s.Phase == model.StrongPhaseReview {
				reviewOK = true
			}
			if s.Phase == model.StrongPhaseRewrite {
				rewriteOK = true
				majorDissent = s.ReviewMajorDissent
			}
		}
	}
	switch {
	case in.Run.ExecutionBlockedReason != "":
		out.Result = "blocked_by_plan_contract"
	case in.Run.Status == store.StrongCompleted || in.Run.Status == store.StrongUnresolved:
		if !reviewOK || !rewriteOK || !terminalVerified || in.Run.Reserved != 0 || !in.BudgetCheck.ChargeReconciles || !in.BudgetCheck.ReservationReconciles || majorDissent == nil || *majorDissent != (in.Run.Status == store.StrongUnresolved) {
			out.Result = "terminal_evidence_unverified"
		} else if *majorDissent {
			out.Result = "review_and_rewrite_with_unresolved_issues"
		} else {
			out.Result = "review_and_rewrite_validated"
		}
	case in.Run.Status == store.StrongBlocked:
		out.Result = "blocked"
	case in.Run.Status == store.StrongCanceled:
		out.Result = "canceled"
	case in.Run.Status == store.StrongSuperseded:
		out.Result = "superseded"
	case in.Run.Status == store.StrongCheckpoint:
		out.Result = "review_not_open"
	case in.Run.Status == store.StrongAwaiting:
		out.Result = "awaiting_approval"
	case in.Run.Status == store.StrongRunning:
		out.Result = "incomplete"
	default:
		out.Result = "unknown"
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return StrongExport{}, err
	}
	return StrongExport{Report: out, ReportSHA256: model.StrongArtifactDigest(raw)}, nil
}
