package prompt

import (
	"embed"
	"fmt"

	"cardex/internal/model"
)

//go:embed templates/*.md
var prefaces embed.FS

// Preface returns the embedded instructions for a card kind.
func Preface(kind model.CardKind) (string, error) {
	if kind == model.KindRanking {
		return "", fmt.Errorf("prompt: ranking preface is not in this lane")
	}
	name := "templates/" + string(kind) + ".md"
	raw, err := prefaces.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("prompt: preface %s: %w", kind, err)
	}
	return string(raw), nil
}
