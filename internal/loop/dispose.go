package loop

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"cardex/internal/harvest"
	"cardex/internal/merge"
	"cardex/internal/model"
	"cardex/internal/store"
)

// Dispose applies one harvested result. It reads only the ## 复核结论 section:
// pass enqueues the reviewed card, rework sends that card back, and an earlier
// ## 结论 does not supply the verdict. A passed dev card gets a review card.
// Other results, including design output, are accepted so the caller can
// consume them. Sched does not parse the body.
func Dispose(ctx context.Context, db *store.Store, card *model.Card, res harvest.Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if db == nil || card == nil {
		return fmt.Errorf("loop: disposition missing card")
	}
	if card.Kind == model.KindReview {
		return disposeReview(ctx, db, card, res.ResultText)
	}
	if res.Outcome == model.OutcomePassed && card.Kind == model.KindDev {
		return ensureDevReview(ctx, db, card)
	}
	return nil
}

func disposeReview(ctx context.Context, db *store.Store, card *model.Card, raw string) error {
	verdict, opinion, ok := reviewVerdict(raw)
	if !ok || len(card.Deps) == 0 {
		card.Status = model.CardNeedsInput
		return nil
	}
	subjectID := card.Deps[0]
	switch verdict {
	case "pass":
		if err := merge.New(db).Enqueue(ctx, subjectID); err != nil {
			return err
		}
	case "rework":
		subject, err := db.GetCard(ctx, subjectID)
		if err != nil {
			return err
		}
		subject.Status = model.CardReady
		subject.ReworkCount++
		subject.WaitReason = ""
		if opinion != "" {
			subject.ReworkNote = opinion
		}
		if err := db.UpdateCard(ctx, &subject); err != nil {
			return err
		}
	default:
		card.Status = model.CardNeedsInput
		return nil
	}
	card.Status = model.CardDone
	return nil
}

func ensureDevReview(ctx context.Context, db *store.Store, card *model.Card) error {
	cards, err := db.ListCards(ctx, card.Objective)
	if err != nil {
		return err
	}
	for _, other := range cards {
		if other.Kind == model.KindReview && hasDep(other, card.ID) {
			card.Status = model.CardReviewing
			return nil
		}
	}
	review := model.Card{
		Objective:    card.Objective,
		Node:         card.Node,
		Kind:         model.KindReview,
		Title:        "review " + card.Title,
		Tier:         model.TierT3,
		Status:       model.CardReady,
		Deps:         []string{card.ID},
		AuthorFamily: card.AuthorFamily,
	}
	if err := db.CreateCard(ctx, &review); err != nil {
		return err
	}
	card.Status = model.CardReviewing
	return nil
}

func hasDep(card model.Card, id string) bool {
	for _, dep := range card.Deps {
		if dep == id {
			return true
		}
	}
	return false
}

// reviewVerdict returns the first pass or rework word in ## 复核结论 only.
func reviewVerdict(raw string) (verdict, opinion string, ok bool) {
	body, found := sectionBody(raw, "复核结论")
	if !found {
		return "", "", false
	}
	fields := strings.Fields(body)
	for i, field := range fields {
		field = strings.Trim(strings.ToLower(field), "`*_.,;:()[]\"'")
		if field != "pass" && field != "rework" {
			continue
		}
		return field, strings.TrimSpace(strings.Join(fields[i+1:], " ")), true
	}
	return "", "", false
}

func sectionBody(raw, name string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	start := -1
	want := normHeading(name)
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if !strings.HasPrefix(trim, "#") {
			continue
		}
		title := strings.TrimSpace(strings.TrimLeft(trim, "#"))
		if normHeading(title) == want {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return "", false
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n"), true
}

func normHeading(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if !unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
