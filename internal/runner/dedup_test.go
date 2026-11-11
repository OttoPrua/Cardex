package runner

import (
	"encoding/json"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

// TestDedupedRunnerHelpers checks the single class patterns and JSON
// string decoder the merge left in this package.
func TestDedupedRunnerHelpers(t *testing.T) {
	cases := []struct {
		name      string
		phrase    string
		wantClass model.ErrorClass
		payload   json.RawMessage
		wantText  string
	}{
		{name: "auth", phrase: "authentication failed", wantClass: model.ErrorAuthFailed},
		{name: "permission", phrase: "permission denied", wantClass: model.ErrorPermissionDenied},
		{name: "timeout", phrase: "context deadline exceeded", wantClass: model.ErrorTimeout},
		{name: "json-string", payload: json.RawMessage(`"kept string"`), wantText: "kept string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.payload != nil {
				if got := jsonString(tc.payload); got != tc.wantText {
					t.Fatalf("jsonString(%s) = %q, want %q", tc.payload, got, tc.wantText)
				}
				return
			}
			if got := ClassifyOutput(tc.phrase); got != tc.wantClass {
				t.Fatalf("ClassifyOutput(%q) = %q, want %q", tc.phrase, got, tc.wantClass)
			}
			if got := classifyScanned([]byte(tc.phrase+"\n"), "", nil, nil); got != tc.wantClass {
				t.Fatalf("classifyScanned(%q) = %q, want %q", tc.phrase, got, tc.wantClass)
			}
		})
	}
}
