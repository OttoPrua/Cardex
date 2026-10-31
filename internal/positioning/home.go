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

// versionRowID was the model-row carrier for an empty table version. New publishes
// store that version in positioning_meta. List and Get do not return this id.
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
	published, ok, err := h.db.PositioningPublishedVersion(ctx)
	if err != nil {
		return nil, err
	}
	visible := withoutVersionRow(rows)
	if len(visible) == 0 {
		if ok || len(rows) > 0 {
			return []model.Positioning{}, nil
		}
		return h.seedLocked()
	}
	ver := maxVersion(visible)
	if ok && published > ver {
		ver = published
	}
	return stampVersion(visible, ver), nil
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
	softened, review := softenPositioningTiers(cloneEntries(p.Entries))
	if err := h.publish(ctx, softened); err != nil {
		return err
	}
	if review {
		p.Status = "待复核"
		if !strings.Contains(p.Diff, "待复核") {
			if p.Diff != "" && !strings.HasSuffix(p.Diff, "\n") {
				p.Diff += "\n"
			}
			p.Diff += "待复核\n"
		}
	} else {
		p.Status = "approved"
	}
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
	published, ok, err := h.db.PositioningPublishedVersion(ctx)
	if err != nil {
		return err
	}
	prev := 0
	if ok {
		prev = published
	}
	if v := maxVersion(rows); v > prev {
		prev = v
	}
	if prev == 0 {
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
	if err := h.db.SetPositioningPublishedVersion(ctx, next); err != nil {
		return err
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

func softenPositioningTiers(entries []model.Positioning) ([]model.Positioning, bool) {
	review := false
	for i := range entries {
		var tiers []model.Tier
		for _, tier := range entries[i].Tiers {
			canon, ok := model.CanonicalTier(tier)
			if ok {
				tiers = append(tiers, canon)
				continue
			}
			if strings.TrimSpace(string(tier)) != "" {
				review = true
			}
		}
		if tiers == nil {
			tiers = []model.Tier{}
		}
		entries[i].Tiers = tiers
	}
	return entries, review
}

func sameTiers(a, b []model.Tier) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func joinTiers(tiers []model.Tier) string {
	parts := make([]string, len(tiers))
	for i, tier := range tiers {
		parts[i] = string(tier)
	}
	return strings.Join(parts, ",")
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
		if old.Family != e.Family {
			fmt.Fprintf(&b, "~ %s family %s -> %s\n", e.Model, old.Family, e.Family)
		}
		if old.Preference != e.Preference {
			fmt.Fprintf(&b, "~ %s preference %d -> %d\n", e.Model, old.Preference, e.Preference)
		}
		if !sameTiers(old.Tiers, e.Tiers) {
			fmt.Fprintf(&b, "~ %s tier %s -> %s\n", e.Model, joinTiers(old.Tiers), joinTiers(e.Tiers))
		}
		if !sameStrings(old.Capabilities, e.Capabilities) {
			fmt.Fprintf(&b, "~ %s capability %s -> %s\n", e.Model, strings.Join(old.Capabilities, ","), strings.Join(e.Capabilities, ","))
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
