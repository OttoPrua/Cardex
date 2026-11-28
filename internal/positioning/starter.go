package positioning

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
)

// Approved reports whether any positioning version has been published to the
// database. The scheduler routes only from published rows; the config.json seed
// is shown by Current but is never routed until a proposal is approved.
func (h *Home) Approved(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.approvedLocked(ctx)
}

func (h *Home) approvedLocked(ctx context.Context) (bool, error) {
	published, err := h.independentVersion()
	if err != nil {
		return false, err
	}
	if published > 0 {
		return true, nil
	}
	rows, err := h.db.ListPositioning(ctx)
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

// Pending returns proposals still waiting for approval, oldest id first.
func (h *Home) Pending() ([]Proposal, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pendingLocked()
}

func (h *Home) pendingLocked() ([]Proposal, error) {
	dir := filepath.Join(h.dir, "positioning", "proposals")
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Proposal{}, nil
		}
		return nil, err
	}
	out := []Proposal{}
	for _, ent := range ents {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			return nil, err
		}
		var p savedProposal
		if json.Unmarshal(b, &p) != nil {
			continue
		}
		if (p.Status == "proposed" || p.Status == "") && p.TierConvention == ProposalTierWire {
			out = append(out, Proposal{ID: p.ID, Entries: TranslateProposalWireTiers(p.Entries), Diff: p.Diff})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// StarterEntries is the table a fresh install is offered: the config.json seed
// when it has model rows, otherwise one row per model the channels offer, each
// usable for T1-T3 at preference 1.
func (h *Home) StarterEntries(channels []model.Channel) ([]model.Positioning, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	seed, err := h.seedLocked()
	if err != nil {
		return nil, err
	}
	if len(withoutVersionRow(seed)) > 0 {
		return withoutVersionRow(seed), nil
	}
	return FromChannels(channels), nil
}

// EnsureStarter proposes the starter table once on a fresh install: no version
// has been published and no proposal is pending. It returns the pending
// proposal (new or existing) and whether it was created now. An empty table
// returns ok=false and proposes nothing.
func (h *Home) EnsureStarter(ctx context.Context, channels []model.Channel) (Proposal, bool, bool, error) {
	approved, err := h.Approved(ctx)
	if err != nil || approved {
		return Proposal{}, false, false, err
	}
	pending, err := h.Pending()
	if err != nil {
		return Proposal{}, false, false, err
	}
	if len(pending) > 0 {
		return pending[len(pending)-1], false, true, nil
	}
	entries, err := h.StarterEntries(channels)
	if err != nil || len(entries) == 0 {
		return Proposal{}, false, false, err
	}
	p, err := h.Propose(ctx, entries)
	if err != nil {
		return Proposal{}, false, false, err
	}
	return p, true, true, nil
}

// FromChannels builds one positioning row per distinct model the channels offer.
func FromChannels(channels []model.Channel) []model.Positioning {
	seen := map[string]bool{}
	out := []model.Positioning{}
	ordered := append([]model.Channel(nil), channels...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	for _, ch := range ordered {
		for _, name := range ch.Models {
			name = strings.TrimSpace(name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			fam := ch.Family
			if fam == "" {
				fam = FamilyOf(name, string(ch.Runner))
			}
			out = append(out, model.Positioning{
				ID:           "starter-" + slug(name),
				Model:        name,
				Family:       fam,
				Tiers:        []model.Tier{model.TierT1, model.TierT2, model.TierT3},
				Capabilities: []string{},
				Preference:   1,
			})
		}
	}
	return out
}

// FamilyOf guesses a model family from the model name, then the runner.
func FamilyOf(modelName, runner string) string {
	m := strings.ToLower(modelName)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	for _, pair := range [][2]string{
		{"gpt", "gpt"}, {"o1", "gpt"}, {"o3", "gpt"}, {"o4", "gpt"}, {"codex", "gpt"},
		{"claude", "claude"}, {"opus", "claude"}, {"sonnet", "claude"}, {"haiku", "claude"},
		{"gemini", "gemini"}, {"grok", "grok"}, {"kimi", "kimi"}, {"k2", "kimi"}, {"k3", "kimi"},
		{"minimax", "minimax"}, {"glm", "glm"}, {"deepseek", "deepseek"}, {"qwen", "qwen"},
	} {
		if strings.HasPrefix(m, pair[0]) {
			return pair[1]
		}
	}
	switch runner {
	case "codex":
		return "gpt"
	case "claude":
		return "claude"
	case "agy":
		return "gemini"
	case "grok", "kimi":
		return runner
	}
	return runner
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// Channels lists the channels configured in this home.
func (h *Home) Channels(ctx context.Context) ([]model.Channel, error) {
	return h.db.ListChannels(ctx)
}
