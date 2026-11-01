package prompt

import (
	"embed"
	"fmt"

	"cardex/internal/model"
)

//go:embed templates/*.md
var prefaces embed.FS

// Preface returns the embedded instructions for a card kind.
// A kind with no template fails the same way as any other missing file.
func Preface(kind model.CardKind) (string, error) {
	name := "templates/" + string(kind) + ".md"
	raw, err := prefaces.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("prompt: preface %s: %w", kind, err)
	}
	return string(raw), nil
}
