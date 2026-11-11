package claims

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
)

// Conflicts reports scheduling conflicts. Review cards do not occupy claims.
// A card with no claims does not conflict. A directory claim is a path with a
// trailing separator. It covers only a new file claimed by exact path under
// that directory. exists reports whether a path is already in the tree;
// an existing file is not covered, including a file-level claim whose symbol
// is empty. A nil exists does not treat an empty symbol as "new". Same-path
// and symbol rules do not use exists.
func Conflicts(cards []model.Card, exists func(path string) bool) ([]Conflict, error) {
	type held struct {
		card  string
		claim model.Claim
	}
	var active []held
	var out []Conflict
	for _, card := range cards {
		if card.Kind == model.KindReview || len(card.Claims) == 0 {
			continue
		}
		for _, claim := range card.Claims {
			for _, other := range active {
				path, symbol, ok := overlap(other.claim, claim, exists)
				if !ok {
					continue
				}
				out = append(out, Conflict{
					Card:   card.ID,
					Other:  other.card,
					Path:   path,
					Symbol: symbol,
					Reason: reason(other.card, path, symbol),
				})
			}
		}
		for _, claim := range card.Claims {
			if strings.TrimSpace(claim.Path) == "" {
				continue
			}
			active = append(active, held{card: card.ID, claim: claim})
		}
	}
	return out, nil
}

func reason(card, path, symbol string) string {
	path = filepath.ToSlash(path)
	if symbol == "" {
		return fmt.Sprintf("被卡 %s 占用 %s", card, path)
	}
	return fmt.Sprintf("被卡 %s 占用 %s:%s", card, path, symbol)
}

func overlap(a, b model.Claim, exists func(string) bool) (path, symbol string, ok bool) {
	ap, ad := normClaimPath(a.Path)
	bp, bd := normClaimPath(b.Path)
	if ap == "" || bp == "" {
		return "", "", false
	}
	if ad || bd {
		return directoryOverlap(a, b, ap, bp, ad, bd, exists)
	}
	if ap != bp {
		return "", "", false
	}
	as := strings.TrimSpace(a.Symbol)
	bs := strings.TrimSpace(b.Symbol)
	if as != "" && bs != "" && as != bs {
		return "", "", false
	}
	sym := as
	if sym == "" {
		sym = bs
	}
	return ap, sym, true
}

func directoryOverlap(a, b model.Claim, ap, bp string, ad, bd bool, exists func(string) bool) (string, string, bool) {
	if ad && bd {
		if underDir(ap, bp) || underDir(bp, ap) {
			dir := ap
			if len(bp) < len(dir) {
				dir = bp
			}
			return dir + "/", "", true
		}
		return "", "", false
	}
	dir, file := ap, bp
	fileClaim := b
	if bd && !ad {
		dir, file = bp, ap
		fileClaim = a
	}
	if !underDir(dir, file) || file == dir {
		return "", "", false
	}
	// New versus existing comes from exists, not from an empty symbol.
	// Nil, or a path that is already in the tree, is not covered.
	if exists == nil || exists(file) {
		return "", "", false
	}
	// A new file is claimed by exact path. A symbol claim is not one.
	if strings.TrimSpace(fileClaim.Symbol) != "" {
		return "", "", false
	}
	return file, "", true
}

func normClaimPath(p string) (string, bool) {
	p = strings.TrimSpace(filepath.ToSlash(p))
	dir := strings.HasSuffix(p, "/")
	p = strings.Trim(p, "/")
	return p, dir
}

func underDir(dir, path string) bool {
	if dir == "" {
		return path != ""
	}
	return path == dir || strings.HasPrefix(path, dir+"/")
}
