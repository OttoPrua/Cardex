package positioning

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cardex/internal/model"
	"cardex/internal/store"
)

// versionRowID stores the published table version when the approved table has
// no model rows. Current leaves it out. The positioning table has no version of its own.
const versionRowID = "cardex.positioning.version"

// publishAfterRead runs inside the publish transaction after the stored version
// is read and before rows are replaced. Tests use it to overlap two approvals.
var publishAfterRead func()

// Home is a versioned positioning table rooted at a caller-supplied directory.
// The unapproved seed is config.json's positioning section. Proposals are files.
// An approval publishes the next table version in one transaction on home/cardex.db.
type Home struct {
	mu  sync.Mutex
	dir string
	db  *store.Store
}

type configFile struct {
	Positioning []model.Positioning `json:"positioning"`
}

type savedProposal struct {
	ID      string              `json:"id"`
	Entries []model.Positioning `json:"entries"`
	Diff    string              `json:"diff"`
	Status  string              `json:"status"`
}

// Open binds a positioning table to home. home must not be ~/.cardex.
func Open(home string) (*Home, error) {
	if err := refuseHome(home); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	db, err := store.Open(filepath.Join(abs, "cardex.db"))
	if err != nil {
		return nil, err
	}
	return &Home{dir: abs, db: db}, nil
}

// Close closes the shared home database.
func (h *Home) Close() error {
	if h == nil || h.db == nil {
		return nil
	}
	return h.db.Close()
}

func refuseHome(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("positioning home is required")
	}
	user, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	banned := filepath.Join(user, ".cardex")
	if abs == banned || strings.HasPrefix(abs, banned+string(filepath.Separator)) {
		return fmt.Errorf("refusing path under ~/.cardex")
	}
	return nil
}

// Current returns the approved version, or the config.json positioning section when nothing has been approved.
func (h *Home) Current(ctx context.Context) ([]model.Positioning, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.currentLocked(ctx)
}

func (h *Home) currentLocked(ctx context.Context) ([]model.Positioning, error) {
	rows, err := h.db.ListPositioning(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return h.seedLocked()
	}
	visible := withoutVersionRow(rows)
	if len(visible) == 0 {
		return []model.Positioning{}, nil
	}
	return stampVersion(visible, maxVersion(visible)), nil
}

func (h *Home) seedLocked() ([]model.Positioning, error) {
	b, err := os.ReadFile(filepath.Join(h.dir, "config.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return []model.Positioning{}, nil
		}
		return nil, err
	}
	var cfg configFile
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	return stampVersion(cfg.Positioning, 0), nil
}

func stampVersion(in []model.Positioning, explicit int) []model.Positioning {
	entries := cloneEntries(in)
	v := explicit
	if v == 0 {
		v = maxVersion(entries)
	}
	if v == 0 && len(entries) > 0 {
		v = 1
	}
	for i := range entries {
		entries[i].Version = v
	}
	if entries == nil {
		entries = []model.Positioning{}
	}
	return entries
}

// Propose stores a proposal and leaves Current unchanged.
func (h *Home) Propose(ctx context.Context, entries []model.Positioning) (Proposal, error) {
	if err := ctx.Err(); err != nil {
		return Proposal{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	cur, err := h.currentLocked(ctx)
	if err != nil {
		return Proposal{}, err
	}
	id, err := newID()
	if err != nil {
		return Proposal{}, err
	}
	cloned := cloneEntries(entries)
	saved := savedProposal{ID: id, Entries: cloned, Diff: summarize(cur, cloned), Status: "proposed"}
	if err := h.writeProposal(saved); err != nil {
		return Proposal{}, err
	}
	return Proposal{ID: saved.ID, Entries: cloneEntries(saved.Entries), Diff: saved.Diff}, nil
}

// Approve makes the proposal the current version. The new version number is greater than the previous one.
func (h *Home) Approve(ctx context.Context, proposalID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	p, err := h.readProposal(proposalID)
	if err != nil {
		return err
	}
	if p.Status != "proposed" && p.Status != "" {
		return fmt.Errorf("proposal %s is %s", proposalID, p.Status)
	}
	if err := h.publish(ctx, cloneEntries(p.Entries)); err != nil {
		return err
	}
	p.Status = "approved"
	return h.writeProposal(p)
}

func (h *Home) publish(ctx context.Context, entries []model.Positioning) error {
	var txErr error
	delay := time.Millisecond
	for i := 0; i < 40; i++ {
		txErr = h.db.WithTx(ctx, func(ctx context.Context) error {
			return h.publishOnce(ctx, entries)
		})
		if txErr == nil || !publishConflict(txErr) {
			return txErr
		}
		if waitErr := sleepCtx(ctx, delay); waitErr != nil {
			return waitErr
		}
		if delay < 50*time.Millisecond {
			delay *= 2
		}
	}
	return txErr
}

func (h *Home) publishOnce(ctx context.Context, entries []model.Positioning) error {
	rows, err := h.db.ListPositioning(ctx)
	if err != nil {
		return err
	}
	prev := 0
	if len(rows) > 0 {
		prev = maxVersion(rows)
	} else {
		seed, err := h.seedLocked()
		if err != nil {
			return err
		}
		if len(seed) > 0 {
			prev = seed[0].Version
		}
	}
	if publishAfterRead != nil {
		publishAfterRead()
	}
	next := prev + 1
	for _, row := range rows {
		if err := h.db.DeletePositioning(ctx, row.ID); err != nil {
			return err
		}
	}
	if len(entries) == 0 {
		carrier := model.Positioning{ID: versionRowID, Version: next}
		return h.db.CreatePositioning(ctx, &carrier)
	}
	for i := range entries {
		entries[i].Version = next
		if entries[i].ID == versionRowID {
			entries[i].ID = ""
		}
		if err := h.db.CreatePositioning(ctx, &entries[i]); err != nil {
			return err
		}
	}
	return nil
}

func publishConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "database is locked")
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func withoutVersionRow(rows []model.Positioning) []model.Positioning {
	out := make([]model.Positioning, 0, len(rows))
	for _, row := range rows {
		if row.ID == versionRowID {
			continue
		}
		out = append(out, row)
	}
	return out
}

// Reject is unspecified. It does not change Current.
func (h *Home) Reject(ctx context.Context, proposalID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if proposalID == "" {
		return fmt.Errorf("proposal id required")
	}
	return fmt.Errorf("positioning reject is unspecified")
}

func (h *Home) writeProposal(p savedProposal) error {
	dir := filepath.Join(h.dir, "positioning", "proposals")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, p.ID+".json"), body, 0o644)
}

func (h *Home) readProposal(id string) (savedProposal, error) {
	if id == "" || strings.Contains(id, "/") || strings.Contains(id, "..") {
		return savedProposal{}, fmt.Errorf("invalid proposal id")
	}
	b, err := os.ReadFile(filepath.Join(h.dir, "positioning", "proposals", id+".json"))
	if err != nil {
		return savedProposal{}, err
	}
	var p savedProposal
	if err := json.Unmarshal(b, &p); err != nil {
		return savedProposal{}, err
	}
	return p, nil
}

func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func maxVersion(entries []model.Positioning) int {
	v := 0
	for _, e := range entries {
		if e.Version > v {
			v = e.Version
		}
	}
	return v
}

func cloneEntries(in []model.Positioning) []model.Positioning {
	if in == nil {
		return nil
	}
	out := make([]model.Positioning, len(in))
	for i, e := range in {
		out[i] = e
		out[i].Tiers = append([]model.Tier(nil), e.Tiers...)
		out[i].Capabilities = append([]string(nil), e.Capabilities...)
	}
	return out
}

func summarize(cur, next []model.Positioning) string {
	have := map[string]model.Positioning{}
	for _, e := range cur {
		have[e.Model] = e
	}
	seen := map[string]bool{}
	var b strings.Builder
	for _, e := range next {
		seen[e.Model] = true
		old, ok := have[e.Model]
		if !ok {
			fmt.Fprintf(&b, "+ %s\n", e.Model)
			continue
		}
		if old.Family != e.Family || old.Preference != e.Preference {
			fmt.Fprintf(&b, "~ %s\n", e.Model)
		}
	}
	for _, e := range cur {
		if !seen[e.Model] {
			fmt.Fprintf(&b, "- %s\n", e.Model)
		}
	}
	return b.String()
}

var _ Store = (*Home)(nil)
