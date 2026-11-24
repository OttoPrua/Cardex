package model

import "testing"

func TestReviewSubject(t *testing.T) {
	cases := []struct {
		name     string
		card     Card
		id       string
		explicit bool
	}{
		{name: "own id", card: Card{ID: "solo", Kind: KindReview}, id: "solo"},
		{name: "rv prefix", card: Card{ID: "rv-dev", Kind: KindReview}, id: "dev"},
		{name: "prefix only", card: Card{ID: "rv-", Kind: KindReview}, id: "rv-"},
		{name: "explicit dep", card: Card{ID: "rv-dev", Kind: KindReview, Deps: []string{"other"}}, id: "other", explicit: true},
		{name: "blank dep falls through", card: Card{ID: "rv-dev", Kind: KindReview, Deps: []string{"  "}}, id: "dev"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, explicit := ReviewSubject(tc.card)
			if id != tc.id || explicit != tc.explicit {
				t.Fatalf("subject %q explicit %v, want %q explicit %v", id, explicit, tc.id, tc.explicit)
			}
		})
	}
}
