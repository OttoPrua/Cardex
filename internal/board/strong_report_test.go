package board

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestStrongRedactedReportPositiveAllowlist(t *testing.T) {
	secret := []string{"/vol-private-canary/a.go", "acct-CANARY", "PROMPT-CANARY", "attempt-CANARY", "commit-CANARY", "identifier-CANARY", "SHA-CANARY", "reason-CANARY", "private-source-code"}
	q := int64(1)
	in := store.StrongRunReport{
		Run:     store.StrongRunView{Key: store.StrongRunKey{Run: secret[5], SHA: secret[6]}, Status: store.StrongBlocked, Reason: secret[7], Approver: secret[1], ApprovalID: secret[5]},
		Version: model.StrongPlanVersion, Template: model.StrongTemplateLight, TaskSHA: secret[2], EvidenceSHA: secret[4],
		Stages: []store.StrongStageReport{
			{Slot: secret[5], State: secret[7], Attempt: secret[3], Channel: secret[1], Model: secret[8], Attempts: 1, ModelDispatches: &q, ModelCalls: &q, ToolCalls: &q,
				InputSHA: secret[2], OutputSHA: secret[6], SyntheticMeasured: &model.Usage{Tokens: 13, Source: "strong_fake_child", Confidence: 1}, Review: map[string]string{secret[0]: secret[8]}, Events: []store.StrongReportEvent{{Type: secret[1], At: secret[4]}}},
			{Attempts: 1, AttemptEstimate: &model.Usage{Tokens: 999, Source: secret[2], Confidence: 0.1}},
		},
	}
	out, err := RedactedStrongReport(in)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range secret {
		if strings.Contains(string(raw), s) {
			t.Fatalf("canary leaked: %s", s)
		}
	}
	body, _ := json.Marshal(out.Report)
	if out.ReportSHA256 != model.StrongArtifactDigest(body) || (out.Report.KnownSyntheticTokens == nil || *out.Report.KnownSyntheticTokens != 13) || out.Report.UnknownUsageAttempts != 1 || out.Report.Attempts != 2 || out.Report.ProviderMeasuredTokens != nil || out.Report.ModelCalls != nil || out.Report.ToolCalls != nil {
		t.Fatalf("redacted aggregation or digest invalid: %s", raw)
	}
	in.Run.Status = store.StrongCompleted
	out, err = RedactedStrongReport(in)
	if err != nil || out.Report.Result != "terminal_evidence_unverified" {
		t.Fatalf("unverified terminal output exported as complete: %+v %v", out, err)
	}
}
