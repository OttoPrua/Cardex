package harvest

import (
	"os"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestSummaryTierWireRetainsOldNumericMeaning(t *testing.T) {
	home := t.TempDir()
	a := model.Attempt{ID: "fixture"}
	path := SummaryPath(home, a)
	if err := writeSummary(home, a, Summary{Tier: model.TierT1, Expected: ExpectedRun{Tier: "T3"}, Match: true}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(after), `"tier": "T3"`) || !strings.Contains(string(after), model.LegacyTierWire) {
		t.Fatalf("summary file lost legacy representation: %s %v", after, err)
	}
	check := func() {
		sum, err := LoadSummary(path)
		if err != nil || sum.Tier != model.TierT1 || sum.Expected.Tier != "T3" || sum.TierConvention != model.TierConvention || !sum.Match {
			t.Fatalf("normalized summary %+v: %v", sum, err)
		}
	}
	check()
	unversioned := []byte(`{"tier":"T3","expected":{"tier":"T1"},"match":true}`)
	if err := os.WriteFile(path, unversioned, 0600); err != nil {
		t.Fatal(err)
	}
	check()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(unversioned) {
		t.Fatalf("read changed history: %s %v", got, err)
	}
}
