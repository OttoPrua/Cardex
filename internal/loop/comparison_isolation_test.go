package loop

import (
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestAPI2StrictComparisonIsNeverOrdinaryResearchInventory(t *testing.T) {
	c := model.Card{Kind: model.KindResearch, Status: model.CardDone, Title: "盘点", Body: "Q0 fixture question",
		Requires: []string{model.RequireStrictComparison}}
	if inventoryCard([]model.Card{c}) != nil {
		t.Fatal("strict result became ordinary inventory input")
	}
}
