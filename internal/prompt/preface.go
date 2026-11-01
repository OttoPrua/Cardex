package prompt

import (
	"embed"
	"strings"

	"cardex/internal/model"
)

//go:embed templates/*.md
var prefaces embed.FS

// Preface returns the embedded instructions for a card kind.
// An empty kind uses the dev preface. A kind with no template is marked
// for review; the card materials still go out with the prompt.
func Preface(kind model.CardKind) (string, error) {
	if strings.TrimSpace(string(kind)) == "" {
		kind = model.KindDev
	}
	name := "templates/" + string(kind) + ".md"
	raw, err := prefaces.ReadFile(name)
	if err != nil {
		return "待复核\n", nil
	}
	return string(raw), nil
}
